# Dev app icon

Debug uses the app logo plus Memex's exact orange bottom DEV banner.
The banner is 230 pixels high on a 1024px icon, with centered white DEV text.
Release artwork and build identity are unchanged.

This app previously had no original icon. Its Dev-only source is a neutral system-symbol placeholder saved in `assets/brand/ios-icon-source.png`.

Regenerate with the shared [ios-dev-icons skill](https://github.com/alexmorris10x/10x-home/tree/main/.agents/skills/ios-dev-icons):

```sh
~/bin/ios-dev-icon --catalog assets/brand/ios-icon-source.png ios/XcodeCellularProbe/DevAssets.xcassets/AppIcon-Dev.appiconset
```

Append `--check` to verify every output. Commit generated PNGs; app builds do not require the helper.

This app's Debug build still uses its existing bundle identity; adding artwork alone does not create a separate Dev data environment.
