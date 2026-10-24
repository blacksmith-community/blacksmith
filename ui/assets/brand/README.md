# Blacksmith forge identity

The anvil and spark master was generated with the built-in `image_gen` tool.
Platform assets are deterministic ImageMagick resizes of that master, with no
artistic edits. The opaque square keeps the mark inside the maskable safe area.

Files: `forge-master.png` (1254 px), `icon-16.png`, `icon-32.png`, `icon-48.png`,
`icon-180.png` (Apple touch icon), `icon-192.png`, and `icon-512.png` (Android,
desktop, and maskable PWA icons). `../../favicon.ico` contains 16/32/48 px images.
The manifest is `../../manifest.webmanifest`. Installation availability depends
on the browser and HTTPS deployment; no offline caching of console data is added.

To regenerate exports, run `magick forge-master.png -resize 192x192 icon-192.png`
for each size, then `magick icon-16.png icon-32.png icon-48.png ../../favicon.ico`.

## Generation prompt

```text
Use case: logo-brand
Asset type: master application icon and brand mark for the Blacksmith infrastructure management console, exported to favicon, Android adaptive/maskable, iOS home screen and desktop PWA icons.
Primary request: Create one exceptionally clear, distinctive modern blacksmith anvil mark with a small single four-point forging spark above its working face. A bold warm-ivory anvil silhouette, with restrained molten copper-orange detail on the top working edge and spark, centered on a solid deep charcoal-iron square background (#171B20). Strong engineered geometry, flat vector-like graphic, clean pixel edges, broad shapes, balanced negative space, readable at 16 pixels. The anvil has a recognizable long tapered horn, sturdy waist and broad base; no hammer, no letterforms. The entire symbol including spark must fit inside the central 64 percent of the square (safe for circular maskable app icons). Warm ivory #F7F3EA and copper #E8934A against dark iron. Full-bleed opaque square background, no pre-rounded corners. Professional operations tooling identity; quiet and durable, not a gaming crest. No text, no gradients, no shadows, no metallic texture, no border, no badge frame, no watermark. Output a single square master image, not a contact sheet.
```
