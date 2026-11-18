# Cloud Controller's verdict on catalog schemas, from the checks in its
# lib/services/service_brokers/v2/schema.rb after the size check: a truthy
# $schema, the draft-04 meta-schema, and an empty object validated against
# the schema, where only a validation error is harmless. It doesn't run
# the size check, which measures a schema's JSON as Oj writes it, so the
# unit tests in internal/services/schema_size_test.go are the only check
# of the size limit.
#
# It reads one JSON schema per line on stdin and writes one verdict per
# line, "accept" or "refuse: <reason>". With "fresh" as its argument it
# clears the json-schema gem's cache before each schema. With "shared" it
# never clears the cache, as a Cloud Controller process doesn't, and it
# first files a few schemas with ids that try to take over keys a later
# schema resolves through.
#
# Each verdict runs in a thread of its own, because Cloud Controller checks
# a catalog in a Puma request thread or a job worker's thread, whose stack
# is far shallower than the main thread's. A schema that runs the gem out
# of stack there fails the catalog, since Cloud Controller doesn't rescue
# SystemStackError.
#
# The Gemfile beside this script pins the gems Cloud Controller runs. Run
# it through `make schema-parity`.
require 'bigdecimal'
require 'json'
require 'json-schema'
require 'tmpdir'

mode = ARGV.fetch(0, 'fresh')
abort "usage: oracle.rb fresh|shared, not #{mode}" unless %w[fresh shared].include?(mode)

DRAFT4 = 'http://json-schema.org/draft-04/schema#'.freeze

# The gem resolves a schema without an id against the working directory,
# so the verdicts come from a directory of their own.
Dir.chdir(Dir.mktmpdir('schema-parity'))

# threaded_verdict is the verdict as a Cloud Controller thread reaches it.
def threaded_verdict(schema)
  Thread.new { verdict(schema) }.value
end

def verdict(schema)
  return 'refuse: no $schema' unless schema.is_a?(Hash) && schema['$schema']

  JSON::Validator.schema_reader = JSON::Schema::Reader.new(accept_uri: false, accept_file: false)
  metaschema = JSON.parse(File.read(JSON::Validator.validator_for_name('draft4').metaschema))

  begin
    errors = JSON::Validator.fully_validate(metaschema, schema, errors_as_objects: true)
  rescue StandardError => e
    return "refuse: meta-schema raised #{e.class}: #{e.message}"
  rescue SystemStackError
    return 'refuse: meta-schema raised SystemStackError'
  end
  return "refuse: meta-schema #{errors.first[:message]}" unless errors.empty?

  begin
    JSON::Validator.validate!(schema, {})
  rescue JSON::Schema::ValidationError
    'accept'
  rescue JSON::Schema::SchemaError => e
    "refuse: SchemaError #{e.message}"
  rescue JSON::Schema::ReadRefused => e
    "refuse: ReadRefused #{e.message}"
  rescue StandardError => e
    "refuse: #{e.class}: #{e.message}"
  rescue SystemStackError
    'refuse: SystemStackError'
  else
    'accept'
  end
end

# Each seed files a body with no definitions under an id, so a later
# schema with the same id whose $ref the gem resolves through it is
# refused. The ids are the ones the random schemas and the tests use.
SEED_IDS = [
  DRAFT4, 'urn:x', 'http://example.com/x', 'http://example.com/params', 'http://example.com/valkey-params',
  'a.json', '#', "file://#{Dir.pwd}/",
].freeze

if mode == 'shared'
  SEED_IDS.each do |id|
    threaded_verdict({ '$schema' => DRAFT4, 'id' => id })
    threaded_verdict({ '$schema' => DRAFT4, 'properties' => { 'p' => { 'id' => id } } })
  end
end

$stdout.sync = true
$stdin.each_line do |line|
  line = line.chomp
  next if line.empty?

  JSON::Validator.clear_cache if mode == 'fresh'
  puts threaded_verdict(JSON.parse(line, decimal_class: BigDecimal)).lines.first.to_s.chomp[0, 300]
end
