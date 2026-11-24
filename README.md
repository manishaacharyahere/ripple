# Ripple

Ripple tells you what a shared Terraform module release will do to the teams
that use it, before you tag it.

Shared modules are libraries with a public API: variables, outputs, the
resources they create, and the provider versions they demand. A renamed
variable or a removed resource is a breaking change, but nothing in Terraform
says so, and semver tags are only as honest as the person writing them.

```
$ ripple diff --repo ./terraform-aws-s3-bucket v1.9.0 v1.10.0
Required bump: major
Declared:      v1.9.0 -> v1.10.0 (minor)  under-bumped

  major  provider.aws     minimum major version raised [~> 2.35] -> [>= 3.0, < 4.0]
  major  var.region       removed; callers that set it fail to plan
  ...
```

Exit code `2` means the release is under-bumped, so it works as a CI gate.

## Usage

```
ripple diff [--from v1.2.0 --to v1.3.0] [--json] OLD_DIR NEW_DIR
ripple diff --repo PATH [--path SUBDIR] [--json] OLD_REF NEW_REF
```

## Rules

| Change | Bump |
|---|---|
| Variable removed, made required, added as required, or its type narrowed | major |
| Output removed or made sensitive | major |
| Resource or module call removed with no `moved` block | major (destroyed in every consumer) |
| Minimum provider/Terraform **major** version raised | major |
| Optional variable or output added, default changed, type widened to `any` | minor |
| Resource or module call added; nested module version changed | minor |
| `removed` block with `destroy = false` | minor |
| Minimum version raised within a major | minor |
| Resource renamed via `moved`; minimum version lowered | patch |

In `0.x`, a minor bump is treated as the breaking-change signal.

## Known limits

Ripple reads code, not plans. It does not yet see `count` -> `for_each`
switches, changed `ForceNew` arguments, or a changed default that recreates a
resource. Those need a `terraform plan` per consumer: use `upgrade-plan` below.
Any type change other than widening to `any` is scored major, even when it
only adds an `optional()` attribute.

## What the upgrade really does: `ripple upgrade-plan`

Reading code is not enough: a changed default or a `count` -> `for_each`
switch only shows up in a plan. `upgrade-plan` measures it on a real consumer:

1. Apply the consumer (a module `examples/*` directory) against the **old**
   version, on LocalStack.
2. **Control plan:** re-plan the unchanged version. Anything that shows up is
   emulator drift, not the upgrade, and is subtracted later (reported as `noise`).
3. Keep the consumer's code and state, swap the module underneath to the
   **new** version, and plan.
4. Classify: `clean`, `changes` (creates/updates), `destructive`
   (deletes/replaces), or `plan-failed` (the consumer no longer works).
5. Destroy the baseline.

```
$ ripple upgrade-plan --repo ./my-module --example examples/basic \
    --endpoint http://localhost:4566 v1.0.0 v1.1.0
{"status":"destructive","replace":0,"delete":1,"create":1,"noise":0,
 "changes":[{"address":"module.bucket.aws_s3_bucket.data","actions":["delete"]}, ...]}
```

Exit code `3` means the upgrade would hurt this consumer. It refuses to run
without `--endpoint` and rejects `*.amazonaws.com`; credentials are always
the dummy `test`/`test`, so a misconfiguration cannot touch a real account.

### On Kubernetes

[`deploy/runner.yaml`](deploy/runner.yaml) runs one check as a Job:

- **LocalStack as a native sidecar** in every pod, so parallel checks never
  share or collide on emulated state.
- Non-root, read-only root filesystem, all capabilities dropped, seccomp
  `RuntimeDefault`, no service-account token.
- NetworkPolicy: DNS plus outbound HTTPS only (git, Terraform registry); no
  cluster-internal or metadata (`169.254.0.0/16`) traffic. kind's default
  CNI did not enforce this in testing, so the cluster uses Calico, and
  [`hack/verify-sandbox.sh`](hack/verify-sandbox.sh) proves the policy holds.
- `backoffLimit: 0`: a failed plan is a result, not something to retry.

## The operator: rolling a release out in waves

A `ModuleRelease` says which version goes to which consumers, in what order:

```yaml
apiVersion: ripple.dev/v1alpha1
kind: ModuleRelease
metadata: { name: s3-v5-0-1, namespace: ripple }
spec:
  repo: https://github.com/terraform-aws-modules/terraform-aws-s3-bucket
  from: v5.0.0
  to: v5.0.1
  waves:
    - { name: dev,  consumers: [examples/object] }
    - { name: prod, consumers: [examples/s3-inventory, examples/s3-analytics] }
```

The operator starts one sandboxed check Job per consumer in the first wave,
reads each verdict from the runner's termination message, and only then
starts the next wave. Any `destructive`, `plan-failed`, inconclusive or
missing verdict **halts** the rollout; nothing after that wave ever starts.

```
$ kubectl -n ripple get mrel
NAME                    FROM     TO       PHASE       WAVE
s3-v5-0-1               v5.0.0   v5.0.1   Succeeded   prod
s3-v5-0-1-fail-closed   v5.0.0   v5.0.1   Halted      dev
```

Design choices, each covered by a test:

- **Stateless reconcile.** Every pass rebuilds progress from the Jobs that
  exist; `status` is output only, never input. A stale or tampered status
  cannot make it skip a wave.
- **Deterministic Job names** derived from (release UID, spec, wave,
  consumer). A restarted operator re-creating a Job gets `AlreadyExists`
  instead of a duplicate run; changing `spec.to` starts a fresh set of checks.
- **Fail closed.** A Job that ends without a verdict (OOM, deadline, lost
  pod) halts the rollout instead of passing it.
- **Least privilege.** Namespaced Role; runners get no service-account token.

## Testing the operator against faults

The tests in [`internal/controller`](internal/controller/release_test.go)
borrow from two systems papers on controller reliability:

- From **Sieve** (Sun et al., OSDI '22), which found most controller bugs by
  perturbing what a controller observes: crashes partway through a
  reconcile, reconciles on a **stale cache** that hides existing or finished
  Jobs, and a **lying status**.
- From **Acto** (Gu et al., SOSP '23), which checks operators against
  end-to-end oracles: a randomised test runs 300 seeded schedules of Job
  completions, crashes and stale reads and checks after every step that
  (1) no wave starts before every earlier check passed, (2) there is never
  more than one Job per consumer, and (3) once faults stop, the release ends
  `Succeeded` iff every verdict passed, otherwise `Halted`.

To check the tests themselves, three bugs were planted one at a time
(a missing verdict counts as passing; the next wave starts without waiting;
non-deterministic Job names). Each one failed the suite.

## Running it on kind

```
docker build -t ripple-runner:dev .
kind create cluster --name ripple --config deploy/kind.yaml
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.29.3/manifests/calico.yaml
kind load docker-image ripple-runner:dev --name ripple
kubectl apply -f deploy/crd/ -f deploy/runner.yaml -f deploy/operator.yaml
bash hack/verify-sandbox.sh
kubectl apply -f deploy/examples/s3-release.yaml
kubectl -n ripple get mrel -w
```

On Docker Desktop with cgroup v1, Kubernetes 1.35+ will not start; use
`--image kindest/node:v1.32.8`.

## Research this builds on

- Begoug, Ouni, Chouchen. *How Do IaC Practitioners Update Their Dependencies?
  An Empirical Study on Terraform Module Updates.* MSR 2025.
- Ochoa, Degueule, Falleri, Vinju. *Breaking Bad? Semantic Versioning and
  Impact of Breaking Changes in Maven Central.* EMSE 2021.
- Sun et al. *Automatic Reliability Testing for Cluster Management Controllers.* OSDI 2022.
- Gu et al. *Acto: Automatic End-to-End Testing for Operation Correctness of
  Cloud System Management.* SOSP 2023.

## Evaluation

`eval/run.sh` replays every consecutive release of a module repo and records
the declared bump against the one Ripple requires. See [eval/RESULTS.md](eval/RESULTS.md).

## Not built yet

- Opening upgrade pull requests in consumer repos once a wave passes. Today a
  passing wave is a gate; the PR step needs a GitHub App and real consumer repos.
- Consumers outside the module repo. Checks use the module's own `examples/*`
  as stand-in consumers.
