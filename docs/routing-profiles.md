# API key routing profiles

Routing profiles give each client API key its own credential pool, priorities and model exclusions. They do not change stored credential settings. Requests without an assignment use the `default` master profile. Without any profiles configured, routing behaves as before.

In the dashboard, open **Auth Files → Routing profiles** to edit the master or create a named profile. Save the profiles, then open **Settings → API keys** and choose a profile beside each key. Save settings to apply assignments. Reassign keys before deleting their profile.

For example, the master below prefers accounts 1, 2, 3, then 4. The secondary key can use only accounts 1 and 4, preferring 4:

```yaml
access:
  api-keys:
    - example-master-key
    - example-secondary-key
  api-key-profiles:
    example-secondary-key: secondary
routing:
  strategy: intelligent-fill
  profiles:
    default:
      auths:
        account1.json: {priority: 40}
        account2.json: {priority: 30}
        account3.json: {priority: 20}
        account4.json: {priority: 10}
    secondary:
      allowed-auths: [account1.json, account4.json]
      excluded-models: ["blocked-model-*"]
      auths:
        account1.json: {priority: 10}
        account4.json:
          priority: 90
          excluded-models: ["another-model"]
```

Replace the example keys and filenames with your configured client keys and credentials. Assignments reference existing profiles; invalid references are rejected. An assignment does not create or authenticate an API key: the key must still pass normal access authentication.

## Inheritance and routing

- The implicit master starts with existing account priorities and availability. Named profiles inherit the configured master and override only fields they specify.
- Higher numeric priorities win. The configured routing strategy chooses within the highest available priority tier; failover stays within the profile's permitted accounts. Priorities are not weights. Session affinity retains an eligible binding, but changing the effective profile starts a new binding namespace. Keys have separate profile session namespaces.
- `allowed-auths` replaces the inherited allowlist. Omit it to inherit; `[]` denies all accounts. Without an allowlist at either level, all otherwise eligible accounts are permitted. Future accounts are excluded by an explicit allowlist.
- Auth identifiers match exact credential IDs, indexes or filenames. When multiple override entries match one credential, ID takes precedence over index, then filename. Use the same identifier consistently in the master and overrides.
- An auth override can set `disabled: true`. `disabled: false` clears a master's profile-level disable, but cannot enable a globally disabled credential.
- `excluded-models` can apply to a whole profile or one auth override. Lists replace inherited lists; `[]` clears inherited exclusions. Model names are case-sensitive, with `*` matching any text. Thinking suffixes and resolved aliases are checked too. A credential with an alias pool containing an excluded target is excluded for that alias request.
- Model discovery only lists models supported by an eligible credential in the profile. Temporary cooldowns do not remove models from discovery.
- Config changes hot-reload. They affect subsequent selections; they do not cancel an already running upstream request. Credential quota and cooldown state remain shared across profiles.

Profiles currently support local credential routing only. Configuration combining profiles with CLIProxyAPIHome is rejected; Home's distributed dispatch does not implement this policy.

## Management API

The existing v8 configuration API exposes these values:

- `GET` / `PUT /v8/management/config/routing/profiles`: the profile map, using the YAML field names shown above as JSON keys.
- `GET` / `PUT /v8/management/config/access/api-key-profiles`: the client-key-to-profile map.

Create profiles before assigning keys. These are management-authenticated configuration operations. The deprecated v0 API is unchanged. Release the proxy implementation before the dashboard controls.
