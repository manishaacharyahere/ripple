# Evaluation: semver honesty in popular Terraform modules

Ripple replayed every consecutive release pair of five widely used
`terraform-aws-modules` repositories (snapshot taken 2026-11-23) and compared
the bump each tag declared against the bump the interface change requires.

Reproduce: clone the repos, then
`eval/run.sh ./ripple results.csv repos/{s3-bucket,vpc,eks,rds,security-group}`.
Raw rows are in [results.csv](results.csv).

| Repo | Pairs | Honest | Under-bumped | Over-bumped |
|---|---:|---:|---:|---:|
| s3-bucket | 130 | 90 | 6 | 34 |
| vpc | 244 | 152 | 15 | 77 |
| eks | 330 | 197 | 41 | 92 |
| rds | 149 | 101 | 16 | 32 |
| security-group | 103 | 69 | 9 | 25 |
| **Total** | **956** | **609** | **87** | **260** |

**37 releases (3.9%) shipped a semver-breaking change under a minor or patch
tag; 8 of them were patch releases.** Anyone pinning `~> X.Y` would have
pulled these in automatically.

What broke in those 37 releases (one release can have several):

| Kind | Findings |
|---|---:|
| Variable or output removed | 31 |
| Resource removed without `moved`, so it is destroyed in every consumer | 12 |
| Minimum provider or Terraform major version raised | 8 |
| Output made sensitive | 2 |
| Variable type narrowed | 57 |

31 of the 37 releases contain at least one unambiguous break (a removal, a
destroy or a provider floor raise). The other 6 only narrowed a variable type,
which breaks callers who pass values outside the new type but not everyone.

Examples:

- `vpc v6.0.0 -> v6.0.1` (patch): AWS provider floor raised from 5.x to 6.0.
- `eks v18.20.0 -> v18.20.1` (patch): `iam_role_use_name_prefix` changed from string to bool.
- `rds v4.1.1 -> v4.1.2` (patch): output `db_master_password` removed.
- `vpc v1.70.0 -> v1.71.0` (minor): `aws_vpc_endpoint.sagemaker.runtime` removed with no `moved` block.

The 49 remaining under-bumped pairs needed a minor and got a patch (usually a
new optional variable or a tightened provider minimum). Over-bumping is not a
bug: maintainers often tag a minor for behaviour that the interface does not
show.

## Threats to validity

- Ripple reads code, not plans, so it misses breaks that only a plan shows
  (`count` to `for_each`, ForceNew arguments). The under-bumped count is a
  lower bound for those.
- "Type narrowed" is strict: any caller value outside the new type fails. A
  maintainer may know no caller passed such values.
- Five repos from one organisation; results may not generalise to internal
  module libraries.
