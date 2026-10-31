# Deploying to nevarmiss.com

Every push to `main` runs `.github/workflows/deploy.yml`: tests, build, copy to the VPS, restart.
If the tests fail, nothing is deployed.

## One-time setup

**On the VPS:**

```sh
git clone https://github.com/k-mufti/nuevo-terminal
sudo bash nuevo-terminal/deploy/setup-vps.sh <your-ssh-user>
sudo certbot --nginx -d nevarmiss.com -d www.nevarmiss.com
```

**A deploy key for GitHub** (on your Mac):

```sh
ssh-keygen -t ed25519 -f ~/.ssh/nuevo_deploy -N "" -C "github-actions-nuevo"
ssh-copy-id -i ~/.ssh/nuevo_deploy.pub <your-ssh-user>@<vps-ip>
```

**Repo secrets** (GitHub → Settings → Secrets and variables → Actions), or with `gh`:

```sh
gh secret set VPS_HOST --body "<vps-ip>"
gh secret set VPS_USER --body "<your-ssh-user>"
gh secret set VPS_SSH_KEY < ~/.ssh/nuevo_deploy
```

## Files

| File | Where it goes |
|---|---|
| `nginx-nevarmiss.conf` | `/etc/nginx/sites-available/nevarmiss.com` |
| `nuevo.service` | `/etc/systemd/system/nuevo.service` (app on `127.0.0.1:4000`) |
| `setup-vps.sh` | Run once; creates the `nuevo` user, `/opt/nuevo`, service, nginx site |

Logs: `journalctl -u nuevo -f` · Stories are kept in `/opt/nuevo/data/nuevo.db` across deploys.
