#!/usr/bin/env ruby
# frozen_string_literal: true

# Renders the pinned blacksmith-community forge releases into the fixture
# tree under internal/parity/testdata/forges, the way a broker VM would.
#
#   render-forges.rb [--work DIR] [--forge NAME] [--check]
#
# For every forge and variant listed in variants.yml the script
#
#   1. downloads the pinned release tarball into a fresh directory under
#      the work directory and verifies its checksum,
#   2. renders the forge's *-blacksmith-plans job with bosh-template,
#   3. runs bin/configure-blacksmith against a temporary root, so that
#      service.yml and each plan's plan.yml come out as they do on a VM,
#   4. rewrites the /var/vcap/jobs paths into fixture-relative paths, adds
#      a placeholder for the CA file the broker VM provides, and
#   5. writes the tree to <forge>/<variant>/ in the fixture directory.
#
# It then scans every rendered YAML file for duplicate mapping keys, which
# some YAML libraries refuse and others silently collapse, and exits 1 when
# it finds any that known-duplicates.yml does not list.  With --check it renders into a scratch directory and fails
# when the committed fixtures differ.

require 'bosh/template'
require 'bosh/template/renderer'
require 'digest'
require 'fileutils'
require 'json'
require 'open3'
require 'optparse'
require 'psych'
require 'tmpdir'
require 'yaml'

HERE = File.expand_path(__dir__)
REPO = File.expand_path('../..', HERE)
FIXTURES = File.join(REPO, 'internal/parity/testdata/forges')
RELEASE_URL = 'https://github.com/blacksmith-community/%<name>s-forge-boshrelease/releases/download/v%<version>s/%<name>s-forge-%<version>s.tgz'

def sh!(*cmd)
  out, status = Open3.capture2e(*cmd)
  abort "#{cmd.join(' ')} failed:\n#{out}" unless status.success?
  out
end

def deep_merge(base, overlay)
  return overlay unless base.is_a?(Hash) && overlay.is_a?(Hash)

  base.merge(overlay) { |_key, left, right| deep_merge(left, right) }
end

# Returns a variant's overlay, built on the variant it extends if any.
def variant_overlay(variants, name)
  overlay = (variants.fetch(name) || {}).dup
  parent = overlay.delete('extends')
  parent ? deep_merge(variant_overlay(variants, parent), overlay) : overlay
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

def download(forge, name, work)
  dir = File.join(work, "#{name}-forge-#{forge['version']}")
  FileUtils.mkdir_p(dir)
  tarball = File.join(dir, "#{name}-forge-#{forge['version']}.tgz")
  sh!('curl', '--fail', '--silent', '--show-error', '--location', '--output', tarball,
      format(RELEASE_URL, name: name, version: forge['version']))

  algorithm, expected = forge['checksum'].split(':', 2)
  digest = algorithm == 'sha256' ? Digest::SHA256 : Digest::SHA1
  actual = digest.file(tarball).hexdigest
  abort "#{name}: #{algorithm} #{actual} does not match pinned #{expected}" unless actual == expected

  release = File.join(dir, 'release')
  jobs = File.join(dir, 'job')
  [release, jobs].each { |d| FileUtils.mkdir_p(d) }
  sh!('tar', '-xzf', tarball, '-C', release, '--no-same-owner')
  sh!('tar', '-xzf', File.join(release, 'jobs', "#{forge['job']}.tgz"), '-C', jobs, '--no-same-owner')
  jobs
end

def render_job(job_dir, job_name, properties, dest)
  job_spec = YAML.load_file(File.join(job_dir, 'job.MF'))
  spec = {
    'job' => { 'name' => job_name },
    'index' => 0, 'id' => 'fixture-id', 'name' => 'blacksmith', 'az' => 'z1',
    'bootstrap' => true, 'address' => '10.0.0.10', 'ip' => '10.0.0.10',
    'networks' => {}, 'links' => {},
    'properties' => with_defaults(job_spec, properties)
  }
  renderer = Bosh::Template::Renderer.new(context: JSON.generate(spec))
  job_spec['templates'].each do |source, target|
    from = File.join(job_dir, 'templates', source)
    to = File.join(dest, target)
    FileUtils.mkdir_p(File.dirname(to))
    File.write(to, renderer.render(from))
    File.chmod(File.stat(from).mode & 0o777, to)
  end
end

# Runs bin/configure-blacksmith with its /var/vcap paths pointed at root and
# returns the services tree it produced.
def configure(root, job_name)
  script_path = File.join(root, 'jobs', job_name, 'bin/configure-blacksmith')
  script = File.read(script_path).gsub('/var/vcap/', "#{root}/")
  File.write(script_path, script)
  sh!('bash', script_path)
  Dir.glob(File.join(root, 'data/blacksmith/*-forge/services')).fetch(0)
end

# Rewrites root-relative job paths so fixtures do not depend on where they
# were rendered.
def relativize(path, root)
  text = File.binread(path)
  fixed = text.gsub("#{root}/jobs/", 'jobs/').gsub('/var/vcap/jobs/', 'jobs/')
  File.binwrite(path, fixed) unless fixed == text
end

def copy_tree(from, to)
  FileUtils.mkdir_p(to)
  Dir.glob('**/*', File::FNM_DOTMATCH, base: from).sort.each do |rel|
    next if %w[. ..].include?(File.basename(rel))

    src = File.join(from, rel)
    dst = File.join(to, rel)
    if File.directory?(src)
      FileUtils.mkdir_p(dst)
    else
      FileUtils.mkdir_p(File.dirname(dst))
      FileUtils.cp(src, dst)
      File.chmod(File.stat(src).mode & 0o111 != 0 ? 0o755 : 0o644, dst)
    end
  end
end

def render_variant(name, forge, variant, overlay, placeholders, job_dir, out)
  properties = deep_merge(forge['base'], resolve_placeholders(overlay, placeholders))
  Dir.mktmpdir("render-#{name}-#{variant}-") do |tmp|
    root = File.realpath(tmp)
    job_root = File.join(root, 'jobs', forge['job'])
    render_job(job_dir, forge['job'], properties, job_root)
    services = configure(root, forge['job'])

    FileUtils.rm_rf(out)
    FileUtils.mkdir_p(out)
    copy_tree(services, File.join(out, 'services'))
    %w[config tls].each do |sub|
      src = File.join(job_root, sub)
      copy_tree(src, File.join(out, 'jobs', forge['job'], sub)) if File.directory?(src)
    end
    Dir.glob('**/*', base: out).each do |rel|
      path = File.join(out, rel)
      relativize(path, root) if File.file?(path)
    end
    # The broker VM's own blacksmith job holds the CA that plan manifests read
    # with the file operator.
    ca = File.join(out, 'jobs/blacksmith/config/tls/blacksmith_services_ca')
    FileUtils.mkdir_p(File.dirname(ca))
    File.write(ca, placeholders.fetch('cert'))
    File.write(File.join(out, 'properties.yml'),
               YAML.dump('release' => "#{name}-forge #{forge['version']}",
                         'variant' => variant, 'properties' => properties))
  end
end

# Walks a Psych AST and reports every mapping that repeats a key.
def duplicate_keys(node, path, found)
  case node
  when Psych::Nodes::Mapping
    seen = {}
    node.children.each_slice(2) do |k, v|
      key = k.is_a?(Psych::Nodes::Scalar) ? k.value : nil
      unless key.nil? || key == '<<'
        found << "#{path}.#{key}" if seen[key]
        seen[key] = true
      end
      duplicate_keys(v, "#{path}.#{key}", found)
    end
  else
    (node.children || []).each { |c| duplicate_keys(c, path, found) }
  end
end

def scan_duplicates(dir)
  problems = []
  Dir.glob('**/*.yml', base: dir).sort.each do |rel|
    next if rel.end_with?('properties.yml')

    begin
      stream = Psych.parse_stream(File.read(File.join(dir, rel)))
    rescue Psych::SyntaxError => e
      problems << "#{rel}: does not parse: #{e.message}"
      next
    end
    found = []
    duplicate_keys(stream, '', found)
    found.each { |key| problems << "#{rel}: duplicate key #{key}" }
  end
  problems
end

options = { work: nil, forge: nil, check: false }
OptionParser.new do |o|
  o.on('--work DIR', 'directory for downloads, created if missing') { |v| options[:work] = v }
  o.on('--forge NAME', 'render only this forge') { |v| options[:forge] = v }
  o.on('--check', 'compare against the committed fixtures instead of writing') { options[:check] = true }
end.parse!

config = YAML.load_file(File.join(HERE, 'variants.yml'))
work = options[:work] || Dir.mktmpdir('forge-downloads-')
FileUtils.mkdir_p(work)
target = options[:check] ? Dir.mktmpdir('forge-check-') : FIXTURES
problems = []

config['forges'].each do |name, forge|
  next if options[:forge] && options[:forge] != name

  job_dir = download(forge, name, work)
  forge['variants'].each_key do |variant|
    out = File.join(target, name, variant)
    overlay = variant_overlay(forge['variants'], variant)
    render_variant(name, forge, variant, overlay, config['placeholders'], job_dir, out)
    problems.concat(scan_duplicates(out).map { |p| "#{name}/#{variant}/#{p}" })
    puts "rendered #{name}/#{variant}"
  end
end

known = YAML.load_file(File.join(HERE, 'known-duplicates.yml'))
unknown = problems.reject do |problem|
  known.any? do |entry|
    File.fnmatch?("#{entry['path']}: duplicate key #{entry['key']}", problem, File::FNM_PATHNAME)
  end
end
(problems - unknown).each { |problem| puts "known duplicate: #{problem}" }
unless unknown.empty?
  warn unknown
  abort 'duplicate-key scan failed'
end
puts 'duplicate-key scan clean apart from the known duplicates'

if options[:check]
  out, status = Open3.capture2e('diff', '-r', FIXTURES, target)
  puts out
  exit(status.success? ? 0 : 1)
end
