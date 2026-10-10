# Permission filtering before host scheduling

A scheduler plugin can restrict credentials without replacing the configured host
selector. When `SchedulerPickRequest.SupportsCandidateFiltering` is true, return
`Handled: true` and `AllowedAuthIDs` containing a subset of the supplied candidate
IDs. The host runs its configured selector within that subset, including priority,
fill-first, round-robin, weighted round-robin, and session affinity.

The filter must contain only current candidates. It cannot accompany `AuthID` or
`DelegateBuiltin`. An explicit empty list denies all candidates; a missing or null
list preserves the legacy contract. Malformed filters terminate selection instead
of falling back to the unrestricted pool. Both PascalCase and snake_case response
fields are accepted.

Plugins that need permission checks before credential priority should retain
`SchedulerAcrossPriorities`. Return all authorized priority tiers: the host reuses
an available session binding even after a higher-priority credential recovers,
and applies priority when establishing a new binding or failing over. Permission
filters are evaluated again for each pick, including retries. A revoked or failed
binding cannot escape the current authorized candidate set.

A plugin must check the request capability before returning a filter. Older hosts
ignore unknown response fields, so they must receive the plugin's existing
permission-aware selection rather than a filter-only response. Both the CPA
binary and the billing plugin must be upgraded to enable host-managed scheduling.

Run the loopback acceptance fixture with a plugin-enabled CPA binary and the
updated billing library:

```sh
python3 test/plugin_candidate_filter_e2e.py \
  --binary /path/to/cli-proxy-api \
  --billing /path/to/cpa-key-billing.so \
  --report /tmp/candidate-filter-report.json
```

The fixture creates temporary credentials, configuration, and billing data. It
checks buffered and streamed completions, all three scheduling strategies,
session affinity, same-request failover, and permission denial without contacting
real model providers. An optional `--legacy-binary` checks compatibility with a
previous plugin-enabled CPA v8 binary.
