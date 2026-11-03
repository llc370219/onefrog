# OneFrog — English guide

[Repository](../README.md) · [Documentation index](README.md)


OneFrog is a self-hosted AI ops panel: connect your Linux servers over SSH and let a team of LLM bots inspect and operate them through chat, with per-command approval, clickable choice cards when a bot needs your decision, SSH host-key pinning, a secret vault, lightweight host probes and an asset/subscription ledger.

```bash
curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo bash
```

It needs `curl`; without it, `wget -qO- https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo bash` runs the installer, which installs `curl` via apt/dnf/yum first. Supported: Debian 12/13, Ubuntu 22.04/24.04 and newer, Rocky/AlmaLinux/CentOS Stream 9/10, Fedora; end-of-life releases (e.g. Debian 11, CentOS 7) stop with a clear message.

The installer installs Docker if missing (after a notice; `OF_INSTALL_DOCKER=0` aborts instead), pulls the prebuilt image and prints the URL, username and a random initial password; both must be changed on first login. It listens on plain HTTP on all interfaces by default and warns about it. For real use, put it behind an HTTPS reverse proxy and bind to localhost with `OF_BIND_ADDR=127.0.0.1 OF_PUBLIC_URL=https://your.domain`, because it stores SSH credentials and Docker-published ports bypass host firewalls.

- On first login you choose how commands are approved. **Smart approval** (recommended) runs allowlisted read-only commands directly and asks you before anything the server-side rules or the AI consider risky; it reduces risk but cannot eliminate it. **Off** runs AI commands without asking. Commands derived from server output, files or attachments always ask in smart mode. Give OneFrog a dedicated, least-privilege SSH user, not root.
- Report security issues privately via GitHub (Security → Report a vulnerability), see [SECURITY.md](../SECURITY.md).
- The optional ChatGPT/Codex and Google Antigravity logins reuse another product's official client identity and unofficial internal endpoints. They may violate the provider's terms and can get your account restricted or permanently banned. You must acknowledge this in a dialog before logging in. Google login needs your own `OF_GOOGLE_OAUTH_CLIENT_ID` / `OF_GOOGLE_OAUTH_CLIENT_SECRET`.
- `master.key` lives next to the encrypted data. Use `OF_MASTER_KEY` to keep them apart.
- Without `OF_PUBLIC_URL`, the installer queries `https://ifconfig.me` to print a link.

To upgrade, re-run the install command (with the same `OF_INSTALL_DIR` if you changed it); the account, settings, chats, secrets and login sessions are kept, with credentials and conversation data encrypted on disk. To run a second instance on the same host, use a different `OF_INSTALL_DIR` and `OF_PUBLISH_PORT`; each install dir gets its own compose project and data volume. The SSH host key is shown with its type (ED25519 preferred); check it with `for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done`. Probes installed by the panel live in `/opt/onefrog-probe`; deleting a server in the panel can uninstall its probe, see docs/安装.md for manual removal. The panel checks GitHub for a newer release at startup and every 12 hours. From v0.1.8, after one installer run on a systemd host, Settings can upgrade in place: the container writes a version tag and a root helper pulls the image already pinned in docker-compose.yml. The Docker socket is not mounted. Turn the check off under Settings → 关于与更新 or with `OF_UPDATE_CHECK=0`.

See [DISCLAIMER.md](../DISCLAIMER.md). OneFrog is not affiliated with OpenAI, Google or any other provider.


Development: clone the `dev` branch and follow [CONTRIBUTING.md](../CONTRIBUTING.md). The repository’s `compose.dev.yml` is for local PostgreSQL development only; production uses the installer.
