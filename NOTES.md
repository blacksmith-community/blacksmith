Blacksmith Notes
================

This is just some notes for coordinating development and general
direction.  Nothing in here is set in stone.

Workflow
--------

Cloud Foundry will contact Blacksmith on a `cf create-service`
call, to provision a new service based on the input parameters
from the user (including the desired plan and any user-supplied
configuration like sizing, instance numbers, etc.)

Blacksmith will spin up a goroutine that is responsible for
implementing that request by contacting the configured BOSH
director and posting a new deployment manifest.  This deployment
will be named after the UUID of the new service (according to
Cloud Foundry) and possibly the plan (for easier ops debugging).

At this point, any credentials are generated using Spruce
operators, and stored in the Vault, generating passwords as
necessary.  The `$.meta.service` subtree will be extracted and
returned to the service creator, by way of Cloud Foundry:

```
meta:
  service: {} # defined by forge writer
  vms:
    postgres/0: 10.0.0.2
    postgres/1: 10.0.0.3
```

`$.meta.vms` will be looked up against the final BOSH deployment,
and returned for connectivity concerns (generating DSNs/URIs in
the app itself).

Vault credentials will be namespaced under the UUID of the
service.

In a plan-upgrade scenario, the broker re-generates the manifest
using the UUID and Vault'd secrets of the existing deployment, and
then performs a `bosh deploy` against the director.  BOSH will
properly migrate the persistent disks from old plan disks to new
plan disks, and deploy the data + code to a new deployment.

For service decommission, the broker simply deletes the BOSH
deployment for the service UUID.



Deploying Blacksmith
--------------------

We will provide a BOSH release (blacksmith-boshrelease) that
provides the code and configuration for the broker itself.  In
order to use the broker, operators will need to deploy additional
BOSH releases to the broker deployment, called "forges".  The
Redis Forge, for example, may provide all of the plans for doing
dedicated Redis queues.

Each forge release may provide additional properties that get
merged into the plan definition, like:

```
instance_groups:
  - name: blacksmith
    jobs:
      - name:    blacksmith
        release: blacksmith
      - name:    concourse-forge
        release: concourse-forge
        properties:
          nats: 10.0.0.9:4225
```

The `concourse-forge` is responsible for merging the `nats`
property into the correct part of the plan YAML, so that it is
resolved by the time the blacksmith broker goes to generate a
final deployment manifest using that plan.

This gives us more flexibility in parameterizing plans, with very
little effort.

Forge BOSH releases will put their plan data in
`/var/vcap/jobs/<name>-forge/plans`, and the blacksmith broker
will enumerate those directories to find and register all found
plans in its service catalog.
