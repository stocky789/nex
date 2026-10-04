// SPDX-License-Identifier: AGPL-3.0-only
import adapter from "@sveltejs/adapter-static";

export default {
  kit: {
    adapter: adapter(),
    prerender: {
      // /downloads/ is the release tree nginx serves beside the site, not a page of it.
      handleHttpError: ({ path, message }) => {
        if (!path.startsWith("/downloads/")) throw new Error(message);
      },
    },
  },
};
