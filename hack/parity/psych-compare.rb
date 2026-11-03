#!/usr/bin/env ruby
# frozen_string_literal: true

# Compares two sets of YAML the way the BOSH director reads them.
#
#   psych-compare.rb [--allow RULE]... [--skip-file REL=REASON]... WANT GOT
#
# WANT and GOT are both files or both directories.  For directories the
# script pairs the *.yml and *.yaml files by relative path, and a file that
# only one side has is a difference.  Each file loads with
# YAML.safe_load(permitted_classes: [Date, Time, Symbol], aliases: true),
# which is how the director loads a manifest.  The script then walks both
# trees and reports every path where the Ruby class or the value differs,
# and where a mapping lacks a key or a sequence has a different length.
# Mapping order does not count, and sequence order does.
#
# Each --allow turns on one narrow exemption, and every exempt difference is
# counted and listed apart from the real ones:
#
#   symbol-quoting   a Symbol in WANT and a String in GOT whose text is the
#                    symbol's name with a colon in front.  yaml.v2 wrote
#                    ":x" plain, which Psych reads as a Symbol, and graft
#                    quotes it, which Psych reads as a String.
#   stringified-keys a key that is not a String in WANT and the String of
#                    its text in GOT, with the same value.  Graft decodes
#                    every key to a string.
#   y-strings        true or false in WANT and "y" or "n" in GOT.  yaml.v2
#                    reads a plain y or n as a boolean, and Psych and graft
#                    read a string.
#
# --skip-file REL=REASON leaves out a whole file, and the reason is listed.
#
# It prints paths, classes, and values and never a whole document, and it
# exits 1 on any difference.

require 'date'
require 'time'
require 'yaml'

PERMITTED = [Date, Time, Symbol].freeze
RULES = %w[symbol-quoting stringified-keys y-strings].freeze

$allowed = []
$exempt = Hash.new { |hash, rule| hash[rule] = [] }

def exempt?(rule, line, want, got)
  return false unless $allowed.include?(rule)

  ok = case rule
       when 'symbol-quoting' then want.is_a?(Symbol) && got.is_a?(String) && ":#{want}" == got
       when 'y-strings' then [true, false].include?(want) && %w[y n].include?(got)
       else false
       end
  $exempt[rule] << line if ok
  ok
end

# Returns the loaded document and nil, or nil and the error class name.  The
# error's message is dropped because it can quote the content.
def load_yaml(path)
  [YAML.safe_load(File.read(path), permitted_classes: PERMITTED, aliases: true), nil]
rescue StandardError, Psych::SyntaxError => e
  [nil, e.class.to_s]
end

def summary(value)
  case value
  when Hash then "#{value.class} of #{value.size} keys"
  when Array then "#{value.class} of #{value.size} items"
  else "#{value.class} #{value.inspect}"
  end
end

def same_scalar?(want, got)
  want == got || (want.is_a?(Float) && want.nan? && got.nan?)
end

def path_for(base, key)
  text = key.to_s
  if key.is_a?(String) && text.match?(/\A[\p{L}\p{N}_-]+\z/)
    "#{base}.#{text}"
  else
    "#{base}[#{text.inspect}](#{key.class})"
  end
end

def compare(base, want, got, found)
  if want.class != got.class
    return if exempt?('symbol-quoting', "#{base}: #{summary(want)} against #{summary(got)}", want, got) ||
              exempt?('y-strings', "#{base}: #{summary(want)} against #{summary(got)}", want, got)

    found << "#{base}: got #{summary(got)}, want #{summary(want)}"
  elsif want.is_a?(Hash)
    missing = want.keys - got.keys
    extra = got.keys - want.keys
    if $allowed.include?('stringified-keys')
      missing.dup.each do |key|
        next if key.is_a?(String) || !extra.include?(key.to_s)

        $exempt['stringified-keys'] << "#{path_for(base, key)}: key is #{key.class}, got String #{key.to_s.inspect}"
        compare(path_for(base, key.to_s), want[key], got[key.to_s], found)
        missing.delete(key)
        extra.delete(key.to_s)
      end
    end
    missing.each { |key| found << "#{path_for(base, key)}: missing" }
    extra.each { |key| found << "#{path_for(base, key)}: extra" }
    (want.keys & got.keys).each { |key| compare(path_for(base, key), want[key], got[key], found) }
  elsif want.is_a?(Array)
    found << "#{base}: #{got.length} items, want #{want.length}" if want.length != got.length
    [want.length, got.length].min.times { |index| compare("#{base}[#{index}]", want[index], got[index], found) }
  elsif !same_scalar?(want, got)
    found << "#{base}: value #{got.inspect}, want #{want.inspect}"
  end
end

def yaml_files(dir)
  Dir.glob('**/*.{yml,yaml}', base: dir).sort
end

def pairs(want, got)
  return [[nil, want, got]] if File.file?(want) && File.file?(got)

  unless File.directory?(want) && File.directory?(got)
    abort 'psych-compare: give two files or two directories'
  end

  (yaml_files(want) | yaml_files(got)).map { |rel| [rel, File.join(want, rel), File.join(got, rel)] }
end

skipped = {}
args = ARGV.dup
rest = []
until args.empty?
  arg = args.shift
  case arg
  when '--allow'
    rule = args.shift
    abort "psych-compare: unknown rule #{rule.inspect}" unless RULES.include?(rule)
    $allowed << rule
  when '--skip-file'
    rel, reason = args.shift.to_s.split('=', 2)
    abort 'psych-compare: --skip-file wants REL=REASON' if rel.to_s.empty? || reason.to_s.empty?
    skipped[rel] = reason
  else
    rest << arg
  end
end
abort 'usage: psych-compare.rb [--allow RULE] [--skip-file REL=REASON] WANT GOT' unless rest.length == 2

found = []
pairs(*rest).each do |rel, want, got|
  label = rel || File.basename(want)
  if skipped.key?(label)
    $exempt['skip-file'] << "#{label}: #{skipped[label]}"
    next
  end
  unless File.exist?(want) && File.exist?(got)
    found << "#{label}: only in #{File.exist?(want) ? 'want' : 'got'}"
    next
  end

  want_doc, want_error = load_yaml(want)
  got_doc, got_error = load_yaml(got)
  if want_error || got_error
    found << "#{label}: cannot load#{" want (#{want_error})" if want_error}#{" got (#{got_error})" if got_error}"
    next
  end

  local = []
  compare('$', want_doc, got_doc, local)
  local.each { |line| found << "#{label} #{line}" }
end

found.each { |line| puts line }
$exempt.each { |rule, lines| lines.each { |line| puts "exempt #{rule}: #{line}" } }
count = $exempt.values.sum(&:length)
detail = $exempt.map { |rule, lines| "#{rule} #{lines.length}" }.join(', ')
puts "psych-compare: #{found.length} difference(s), #{count} exempt#{" (#{detail})" unless detail.empty?}"
exit(found.empty? ? 0 : 1)
