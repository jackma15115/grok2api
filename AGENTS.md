# Repository Agent Notes

## Remote Test Host

- Use the existing SSH alias `grok2api-test` for disposable integration and deployment tests.
- The alias is defined in the local SSH config; connect with `ssh grok2api-test`. Do not duplicate its address or private key in repository files.
- Before changing the host, inspect its running containers, occupied ports, disk space, and existing project directories.
- Keep test workloads isolated with task-specific container, network, image, volume, and `/tmp` names. Do not modify or stop unrelated services.
- Never persist user-provided SSO tokens or other credentials in the repository, remote images, shell history, container environment, or long-lived files. Pass them through temporary standard input and remove all temporary state after testing.
- Report which temporary resources were created and confirm their cleanup when the test finishes.
