#!/usr/bin/env ruby
# frozen_string_literal: true

# Renders the broker config the blacksmith release deploys, from the release
# version the kit pins, into internal/config/testdata/kit.
#
#   render-config.rb [--work DIR] [--check]
#
# It downloads the pinned release tarball into a fresh directory under the
# work directory, verifies its checksum, renders config/blacksmith.conf with
# bosh-template once for each variant in config-properties.yml, and writes
# <variant>.conf. With --check it renders into a scratch directory and fails
# when the committed files differ.

require 'bosh/template'
require 'bosh/template/renderer'
require 'digest'
require 'fileutils'
require 'json'
require 'open3'
require 'optparse'
require 'tmpdir'
require 'yaml'

HERE = File.expand_path(__dir__)
REPO = File.expand_path('../..', HERE)
OUTPUT = File.join(REPO, 'internal/config/testdata/kit')
RELEASE_URL = 'https://github.com/blacksmith-community/blacksmith-boshrelease/releases/download/v%<version>s/blacksmith-%<version>s.tgz'

def sh!(*cmd)
  out, status = Open3.capture2e(*cmd)
  abort "#{cmd.join(' ')} failed:\n#{out}" unless status.success?
  out
end

def deep_merge(base, overlay)
  return overlay unless base.is_a?(Hash) && overlay.is_a?(Hash)

  base.merge(overlay) { |_key, left, right| deep_merge(left, right) }
end

def resolve_placeholders(value, placeholders)
  case value
  when Hash then value.transform_values { |v| resolve_placeholders(v, placeholders) }
  when Array then value.map { |v| resolve_placeholders(v, placeholders) }
  when /\A@(\w+)\z/ then placeholders.fetch(Regexp.last_match(1))
  else value
  end
end

def set_nested(hash, dotted, value)
  keys = dotted.split('.')
  leaf = keys.pop
  node = keys.reduce(hash) { |h, k| h[k] ||= {} }
  node[leaf] = value
end

def with_defaults(job_spec, properties)
  defaults = {}
  job_spec.fetch('properties', {}).each do |name, meta|
    set_nested(defaults, name, Marshal.load(Marshal.dump(meta['default']))) if meta.key?('default')
  end
  deep_merge(defaults, properties)
end

def download(release, work)
  dir = File.join(work, "blacksmith-#{release['version']}")
  FileUtils.mkdir_p(dir)
  tarball = File.join(dir, "blacksmith-#{release['version']}.tgz")
  sh!('curl', '--fail', '--silent', '--show-error', '--location', '--output', tarball,
      format(RELEASE_URL, version: release['version']))

  algorithm, expected = release['checksum'].split(':', 2)
  digest = algorithm == 'sha256' ? Digest::SHA256 : Digest::SHA1
  actual = digest.file(tarball).hexdigest
  abort "#{algorithm} #{actual} does not match pinned #{expected}" unless actual == expected

  extracted = File.join(dir, 'release')
  job = File.join(dir, 'job')
  [extracted, job].each { |d| FileUtils.mkdir_p(d) }
  sh!('tar', '-xzf', tarball, '-C', extracted, '--no-same-owner')
  sh!('tar', '-xzf', File.join(extracted, 'jobs', "#{release['job']}.tgz"), '-C', job, '--no-same-owner')
  job
end

options = { work: nil, check: false }
OptionParser.new do |o|
  o.on('--work DIR', 'directory for downloads, created if missing') { |v| options[:work] = v }
  o.on('--check', 'compare against the committed files instead of writing') { options[:check] = true }
end.parse!

config = YAML.load_file(File.join(HERE, 'config-properties.yml'))
work = options[:work] || Dir.mktmpdir('config-downloads-')
FileUtils.mkdir_p(work)
target = options[:check] ? Dir.mktmpdir('config-check-') : OUTPUT
FileUtils.mkdir_p(target)

job_dir = download(config['release'], work)
job_spec = YAML.load_file(File.join(job_dir, 'job.MF'))
template = File.join(job_dir, 'templates/config/blacksmith.conf')

config['variants'].each do |variant, overlay|
  properties = with_defaults(job_spec, resolve_placeholders(overlay, config['placeholders']))
  spec = {
    'job' => { 'name' => 'blacksmith' }, 'deployment' => 'fixture-env-blacksmith',
    'index' => 0, 'id' => 'fixture-id', 'name' => 'blacksmith', 'az' => 'z1',
    'bootstrap' => true, 'address' => '10.0.0.10', 'ip' => '10.0.0.10',
    'networks' => {}, 'links' => {}, 'properties' => properties
  }
  rendered = Bosh::Template::Renderer.new(context: JSON.generate(spec)).render(template)
  File.write(File.join(target, "#{variant}.conf"), rendered)
  puts "rendered #{variant}"
end

if options[:check]
  out, status = Open3.capture2e('diff', '-r', OUTPUT, target)
  puts out
  exit(status.success? ? 0 : 1)
end
