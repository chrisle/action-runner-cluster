# Personal accounts vs organizations

GitHub has no user-level runners. An org registers runners once and every repo
in it can use them; a personal account can only register a runner **on a
specific repository**. arc handles that difference itself — it watches your
repos, and when a job queues it registers a runner against that job's repo and
starts it. What follows from the constraint:

| | Organization (`github.org`) | Personal account (`github.owner`) |
| --- | --- | --- |
| Runner registration | Once, org-wide | Per repo, per job |
| Token | Classic PAT with `admin:org`, or a GitHub App | Classic PAT with `repo` (fine-grained: Administration read/write) |
| GitHub App auth | Supported | Not supported — GitHub Apps cannot administer a user's repo runners |
| Runner groups | Optional (Enterprise Cloud / GHES) | Do not exist; leave `runner_group` empty |
| Warm runners (`min`) | Any value | Must be `0` |
| Webhooks | One org hook | One hook per watched repo |

`min` must be `0` on a personal account because a warm runner has to be
registered *somewhere*, and "somewhere" is one repo — it would sit idle there
while every other repo's jobs queued. Runners are created on demand instead,
which is why [webhooks](webhooks.md) matter more here: a queued job pokes arc
immediately rather than waiting for the next poll.

Because a personal account can own hundreds of repos and each watched repo costs
API traffic, arc only watches repos pushed to in the last 30 days unless you
scope the set yourself with `github.repos`. Forks are skipped unless you set an
`include` list, since they rarely run their own Actions. See
[Configuration](configuration.md#which-repos-get-watched).
