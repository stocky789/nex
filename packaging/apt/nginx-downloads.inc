# SPDX-License-Identifier: AGPL-3.0-only
# Include from the nex-website server block, alongside nginx-apt.inc.

# Release downloads: ISO images and their SHA-256 sidecars (GET/HEAD only).
# autoindex is on here, unlike /apt/, because operators need to find the ISO for
# the version they want; the tree holds only ISOs and checksums.
location /downloads/ {
    alias /srv/nex-downloads/;
    disable_symlinks off if_not_owner;
    autoindex on;
    autoindex_exact_size off;
    absolute_redirect off;
    limit_except GET HEAD { deny all; }
    types {
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
