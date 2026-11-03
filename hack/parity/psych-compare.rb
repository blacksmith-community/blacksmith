#!/usr/bin/env ruby
# frozen_string_literal: true

# Compares two sets of YAML the way the BOSH director reads them.
#
#   psych-compare.rb WANT GOT
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
# It prints paths, classes, and values and never a whole document, and it
# exits 1 on any difference.

require 'date'
require 'time'
require 'yaml'

PERMITTED = [Date, Time, Symbol].freeze

def load_yaml(path)
  YAML.safe_load(File.read(path), permitted_classes: PERMITTED, aliases: true)
rescue StandardError => e
  "load error: #{e.class}"
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
    found << "#{base}: class #{got.class}, want #{want.class} (#{got.inspect} against #{want.inspect})"
  elsif want.is_a?(Hash)
    (want.keys - got.keys).each { |key| found << "#{path_for(base, key)}: missing" }
    (got.keys - want.keys).each { |key| found << "#{path_for(base, key)}: extra" }
    (want.keys & got.keys).each { |key| compare(path_for(base, key), want[key], got[key], found) }
  elsif want.is_a?(Array)
    found << "#{base}: #{got.length} items, want #{want.length}" if want.length != got.length
    [want.length, got.length].min.times { |index| compare("#{base}[#{index}]", want[index], got[index], found) }
  elsif want != got
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

abort 'usage: psych-compare.rb WANT GOT' unless ARGV.length == 2

found = []
pairs(*ARGV).each do |rel, want, got|
  label = rel || File.basename(want)
  unless File.exist?(want) && File.exist?(got)
    found << "#{label}: only in #{File.exist?(want) ? 'want' : 'got'}"
    next
  end

  compare('$', load_yaml(want), load_yaml(got), local = [])
  local.each { |line| found << "#{label} #{line}" }
end

found.each { |line| puts line }
puts "psych-compare: #{found.length} difference(s)"
exit(found.empty? ? 0 : 1)
