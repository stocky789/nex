# Nex website

SvelteKit and custom CSS, prerendered as a static site. Pages, navigation, FAQs and
screenshot links work without JavaScript. Keep the preview private until the
maintainer authorizes publication.

## Local development

Use Node.js 22.12 or newer:

```sh
cd website
npm ci
npm run check
npm run dev -- --host 127.0.0.1
```

## Build and preview

```sh
npm run build
npm run preview -- --host 127.0.0.1
```

Deploy `build/`, including its license files, to a static web server. No Node
runtime is required. Pushes to `main` that touch `website/` run the Website
workflow: it builds the site and, when the `NEX_DEPLOY_*` secrets are set,
publishes through `nex-website-publish` on the website host. Manual publish and
rollback remain available; see [operate the website](../docs/guide/operate-website.md).
The preview uses `noindex, nofollow`, which is a crawler hint, not access control.

## Contributing

Contribute copy, design, accessibility, documentation and screenshots through this
repository's review process. Run the checks and build before submitting. Keep
credentials, deployment configuration and personal infrastructure details outside
this directory. Commit source, the lockfile and public assets; generated output
and dependency caches are ignored.

Gallery images show the real Controller with fictional demo inventory. Use
[the capture script](../tests/integration/website_controller_captures.py) to generate
them; [capture evidence](../docs/qa/website-controller-captures/results.json) records their
provenance and verification. See the [website checks](../docs/qa/website/checks.md)
for browser verification.

## License and assets

Original source and configuration use [AGPL-3.0-only](../LICENSE). The unchanged
`static/nex-brand.png` brand board is displayed using CSS cropping and retains the
separate artwork terms in [Licensing](../docs/LICENSING.md). System fonts are used;
no external fonts, icons or analytics are loaded.

[Third-party notices](static/THIRD-PARTY-NOTICES.txt) retain direct build dependency
license texts. Transitive dependencies are recorded in `package-lock.json` and
installed package license files. Build tools are not deployed; client rendering
is disabled.

The recorded dependency audit has three low-severity entries for
`GHSA-pxg6-pf52-xh8x` (`cookie`, via SvelteKit and its adapter). The affected server
cookie handler is absent from this static deployment. Review the
[security evidence](../docs/qa/website-security/review.md) before adding server
routes; do not apply the suggested obsolete major-version downgrade.
