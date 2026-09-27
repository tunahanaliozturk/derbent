import { themes } from 'prism-react-renderer';

const repo = 'https://github.com/tunahanaliozturk/derbent';

// The deploy workflow passes what GitHub Pages serves the site at, so a custom domain set in the
// repository's Pages settings needs no change here.
const url = process.env.SITE_URL || 'http://localhost:3000';
const baseUrl = process.env.BASE_URL || '/';

export default {
  title: 'Derbent',
  tagline: 'One guarded pass for all your coding agents.',
  favicon: 'derbent-favicon.svg',
  url,
  baseUrl,
  onBrokenLinks: 'throw',
  markdown: { format: 'detect', hooks: { onBrokenMarkdownLinks: 'throw' } },
  staticDirectories: ['static', '../docs/assets'],
  clientModules: ['./src/client/fonts.js'],
  i18n: { defaultLocale: 'en', locales: ['en'] },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.js',
          editUrl: `${repo}/edit/main/website/`,
        },
        blog: {
          path: 'releases',
          routeBasePath: 'releases',
          blogTitle: 'Release notes',
          blogDescription: 'What changed in each Derbent release.',
          blogSidebarTitle: 'Releases',
          blogSidebarCount: 'ALL',
          showReadingTime: false,
          onUntruncatedBlogPosts: 'ignore',
          feedOptions: { type: 'all', title: 'Derbent releases' },
        },
        theme: { customCss: './src/css/custom.css' },
      },
    ],
  ],

  themeConfig: {
    image: 'social-card.png',
    colorMode: { respectPrefersColorScheme: true },
    navbar: {
      title: 'Derbent',
      logo: { alt: 'Derbent', src: 'derbent-mark.svg' },
      items: [
        { type: 'docSidebar', sidebarId: 'docs', label: 'Docs', position: 'left' },
        { to: '/releases', label: 'Releases', position: 'left' },
        { href: repo, label: 'GitHub', position: 'right' },
      ],
    },
    footer: {
      links: [
        {
          title: 'Docs',
          items: [
            { label: 'Introduction', to: '/docs/' },
            { label: 'Installation', to: '/docs/installation' },
            { label: 'Rules', to: '/docs/rules' },
            { label: 'Approvals', to: '/docs/approvals' },
          ],
        },
        {
          title: 'Releases',
          items: [
            { label: 'Release notes', to: '/releases' },
            { label: 'Downloads', href: `${repo}/releases` },
            { label: 'RSS', href: 'pathname:///releases/rss.xml' },
          ],
        },
        {
          title: 'Project',
          items: [
            { label: 'GitHub', href: repo },
            { label: 'Issues', href: `${repo}/issues` },
            { label: 'Licence', href: `${repo}/blob/main/LICENSE` },
          ],
        },
      ],
      copyright: `Apache 2.0 · Copyright © ${new Date().getFullYear()} Tunahan Ali Ozturk`,
    },
    prism: {
      theme: themes.github,
      darkTheme: themes.vsDark,
      additionalLanguages: ['toml', 'powershell'],
    },
  },
};
