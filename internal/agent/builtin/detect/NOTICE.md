# Screen-state rules

The `*.toml` files in this directory are the agent detection manifests of
[herdr](https://github.com/herdrdev/herdr) (`src/detect/manifests/`, commit
`9c38bbc64e3b7a23176afb2451a067a53fb8d2fd`, 2026-10-13), licensed under the
Apache License, Version 2.0 (http://www.apache.org/licenses/LICENSE-2.0).

They are copied unmodified. team-worker reads them to tell, from an agent's
terminal, whether it is working, waiting at its prompt, or blocked on a
question, when the agent's CLI reports nothing through hooks.
