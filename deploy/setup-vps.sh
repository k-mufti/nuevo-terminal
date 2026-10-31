#!/usr/bin/env bash
# One-time VPS setup. Run on the VPS from a clone of the repo:
#   git clone https://github.com/k-mufti/nuevo-terminal && sudo bash nuevo-terminal/deploy/setup-vps.sh <deploy-user>
# <deploy-user> is the SSH user GitHub Actions logs in as. It gets write access
# to /opt/nuevo and permission to restart the service, nothing else.
set -euo pipefail
DEPLOY_USER="${1:?usage: sudo bash setup-vps.sh <deploy-user>}"
HERE="$(cd "$(dirname "$0")" && pwd)"

id "$DEPLOY_USER" >/dev/null || { echo "user $DEPLOY_USER does not exist"; exit 1; }
id nuevo >/dev/null 2>&1 || useradd --system --home /opt/nuevo --shell /usr/sbin/nologin nuevo

mkdir -p /opt/nuevo/data
chown "$DEPLOY_USER":"$DEPLOY_USER" /opt/nuevo
chown -R nuevo:nuevo /opt/nuevo/data

install -m 644 "$HERE/nuevo.service" /etc/systemd/system/nuevo.service
systemctl daemon-reload
systemctl enable nuevo   # starts on boot; first deploy starts it

# Let the deploy user restart the app without a password (only this command).
echo "$DEPLOY_USER ALL=(root) NOPASSWD: /usr/bin/systemctl restart nuevo" > /etc/sudoers.d/nuevo-deploy
chmod 440 /etc/sudoers.d/nuevo-deploy
visudo -cf /etc/sudoers.d/nuevo-deploy

if [ ! -e /etc/nginx/sites-available/nevarmiss.com ]; then
  install -m 644 "$HERE/nginx-nevarmiss.conf" /etc/nginx/sites-available/nevarmiss.com
  ln -sf /etc/nginx/sites-available/nevarmiss.com /etc/nginx/sites-enabled/nevarmiss.com
  nginx -t && systemctl reload nginx
fi

echo
echo "Done. Next:"
echo "  1. sudo certbot --nginx -d nevarmiss.com -d www.nevarmiss.com"
echo "  2. Push to main on GitHub (or run the Deploy workflow by hand) to install the app."
