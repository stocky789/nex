# SPDX-License-Identifier: AGPL-3.0-only
# Include from the nex-website server block, alongside nginx-apt.inc.

# Release downloads (GET/HEAD only). The publisher adds an index to each retained
# version; direct package URLs remain available to the network installer.
location /downloads/ {
    alias /srv/nex-downloads/;
    # `latest` is an atomically switched release symlink owned by the publishing
    # account, so this location must permit that one controlled indirection.
    disable_symlinks off;
    autoindex on;
    autoindex_exact_size off;
    absolute_redirect off;
    limit_except GET HEAD { deny all; }
    types {
        text/html html;
        application/json json;
        application/x-iso9660-image iso;
        text/plain sha256;
    }
    default_type application/octet-stream;
    # Location-level headers replace server add_header inheritance. Omit CSP so
    # download clients are not affected by the website policy.
    add_header Referrer-Policy "no-referrer" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;
}
