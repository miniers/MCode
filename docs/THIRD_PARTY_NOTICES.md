# Third-Party Notices

## Lucide Icons

The icon path data in `internal/ui/icons.go` is derived from [Lucide](https://lucide.dev/).

ISC License

Copyright (c) for portions of Lucide are held by Cole Bemis 2013-2022 as part of Feather (MIT).
All other portions Copyright (c) Lucide Contributors 2022.

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

## Provider Brand Marks

`internal/ui/icons_assets/provider-*.svg` holds the monochrome provider icons (every
`fill`/`stroke` set to `currentColor`, so the icon follows the surrounding text color)
and `internal/ui/icons_assets/color/provider-*.svg` the colour versions drawn through
`ProviderIconColor`. Each mark is a trademark of its owner, used only to identify the
corresponding product or service; no endorsement or affiliation is implied. Path data is
copied as published apart from the fill colour and, where the published file wraps it in
decoration MyGo cannot draw (filters, masks, glows), from the layers the icon needs; the
monochrome file also drops the badge so the mark reads on its own. Every file stays
inside the element subset MyGo draws.

Every mark has been rescaled and centred on a 24×24 canvas with its longest edge at 20
units, so its visible size no longer depends on how much of the published viewBox the
artwork happened to fill; the near-black fills of the colour `cline`, `zai` and `pi` marks
are now `#000000`, which the renderer substitutes with the theme's primary text colour.

### Simple Icons (CC0-1.0)

Taken from [Simple Icons](https://simpleicons.org/), released under CC0-1.0, by slug and
brand colour:

| File | Simple Icons slug | Brand colour |
| --- | --- | --- |
| `provider-claude.svg` | `claude` | `#D97757` |
| `provider-cline.svg` | `cline` | `#18181B` |
| `provider-copilot.svg` | `githubcopilot` | `#000000` |
| `provider-cursor.svg` | `cursor` | `#000000` |
| `provider-deepseek.svg` | `deepseek` | `#5786FE` |
| `provider-meta.svg` | `meta` | `#0467DF` |
| `provider-opencode.svg` | `opencode` | `#000000` |
| `provider-zai.svg` | `zdotai` | `#2D2D2D` |

### Published brand assets of the owner

| File | Source | Note |
| --- | --- | --- |
| `provider-antigravity.svg` | <https://antigravity.google/favicon.svg> | Google Antigravity mark, brand blue `#3186FF` |
| `provider-codex.svg` | <https://developers.openai.com/favicon.svg> | OpenAI blossom mark used for Codex, `#000000`; the published blue `#0080F7` disc is dropped |
| `provider-devin.svg` | <https://devin.ai/favicon.svg> | Devin (Cognition) mark, `#000000` |
| `provider-droid.svg` | <https://docs.factory.ai/favicon.svg> | Factory mark used for Droid, `#FAFAFA` on a rounded `#0A0A0A` badge in the colour version (published square is `#020202`) |
| `provider-grok.svg` | <https://grok.com/images/favicon.svg> | xAI Grok mark, `#FCFCFC` on a `#050505` badge in the colour version |
| `provider-pi.svg` | <https://pi.dev/favicon.svg> | Pi mark, `#111111` |

### mcode original monograms

No published vector source for these providers was available, so mcode draws a neutral
letter monogram: a rounded-square outline carrying the initial. It reproduces no logo.
The monochrome version strokes the whole monogram in `currentColor`; the colour version
puts the white letter on a neutral grey `#3F3F46` badge.

| File | Letter |
| --- | --- |
| `provider-commandcode.svg` | `C` |
| `provider-omp.svg` | `O` |

The Swarm bee images (`internal/ui/icons_assets/swarm-bee-*.svg`) and `svgSwarmMark`
are mcode's own drawings and are not brand marks.

The System Image replacements — `IconSquarePen`, `IconPanelLeft`, `IconGitBranch`,
`IconTerminal`, `IconGlobe`, `IconBookOpen` and `IconPlusMinus` — are hand-drawn
outlines inspired by the look of Apple's SF Symbols. They reproduce no SF
Symbols path data: SF Symbols is copyright Apple Inc. and licensed for use only
within Apple platform software.

## magpie

`internal/magpiecfg` ports parts of magpie's `internal/edit` and `internal/steady`
packages (JSONC/TOML key-path editing, atomic file writes) into mcode. Only the
underlying editing library was ported; no magpie package is imported at build time.

MIT License

Copyright (c) 2026 yetone

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## gjson

Used by `internal/magpiecfg` to look up JSON values by dot-separated key path.

MIT License

Copyright (c) 2016 Josh Baker

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## jsonc

Used by `internal/magpiecfg` to strip JSONC comments and trailing commas in
place before editing a JSON file.

MIT License

Copyright (c) 2021 Josh Baker

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## pelletier/go-toml/v2

Used by `internal/magpiecfg` to parse TOML while keeping comments and table
layout, and to validate the result of an edit before it is written.

MIT License

Copyright (c) 2021 - 2023 Thomas Pelletier

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
