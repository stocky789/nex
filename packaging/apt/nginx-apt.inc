# SPDX-License-Identifier: AGPL-3.0-only
# Include from the nex-website server block, or keep the matching inline section
# in packaging/website/nginx.conf.template.

# APT repository (GET/HEAD only; no directory listing; no symlink follow).
location /apt/ {
    alias /srv/nex-apt/;
    disable_symlinks on;
    autoindex off;
    absolute_redirect off;
    limit_except GET HEAD { deny all; }
    types {
        text/plain Packages;
        application/json json;
        application/octet-stream deb;
    }
    default_type application/octet-stream;
    # Location-level headers replace server add_header inheritance. Omit CSP so
    # apt clients are not affected by the website policy.
    add_header Referrer-Policy "no-referrer" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;
}

location = /apt/conf {
    deny all;
}

location /apt/conf/ {
    deny all;
}

location = /apt/gnupg {
    deny all;
}

location /apt/gnupg/ {
    deny all;
}
