# Changelog

## [2.2.0](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v2.1.1...v2.2.0) (2026-08-11)


### Features

* **editor:** onion-skin overlay, from-scratch canvas with a custom reference, and save-to-library ([3a68595](https://github.com/HorizonRepublic/fh6-paint-studio/commit/3a6859566c4ca72380e80b5f89ca4a61476caf57))
* **editor:** skew handle, independent side resize, and a shape-hugging selection frame ([3a68595](https://github.com/HorizonRepublic/fh6-paint-studio/commit/3a6859566c4ca72380e80b5f89ca4a61476caf57))
* **inject:** faster, restart-safe layer-table locate ([3a68595](https://github.com/HorizonRepublic/fh6-paint-studio/commit/3a6859566c4ca72380e80b5f89ca4a61476caf57))
* **inject:** rebuild vinyl meshes on import so a paste renders without a reload ([3a68595](https://github.com/HorizonRepublic/fh6-paint-studio/commit/3a6859566c4ca72380e80b5f89ca4a61476caf57))


### Performance

* **engine:** parallelise the colour, edge, weight, and polish hotspots ([3a68595](https://github.com/HorizonRepublic/fh6-paint-studio/commit/3a6859566c4ca72380e80b5f89ca4a61476caf57))


### Bug Fixes

* **deps:** update module gioui.org to v0.10.2 ([#61](https://github.com/HorizonRepublic/fh6-paint-studio/issues/61)) ([31c9d70](https://github.com/HorizonRepublic/fh6-paint-studio/commit/31c9d707f35bc932e3c69f83dd92b32c87076ea5))
* **engine:** close a run-registration race and clamp Vulkan candidate counts ([3a68595](https://github.com/HorizonRepublic/fh6-paint-studio/commit/3a6859566c4ca72380e80b5f89ca4a61476caf57))

## [2.1.1](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v2.1.0...v2.1.1) (2026-08-10)


### Bug Fixes

* release the dead-code cleanup and Nexus link ([4709f4b](https://github.com/HorizonRepublic/fh6-paint-studio/commit/4709f4bcbeda06334abdbb8cb122c532f9f246d6))

## [2.1.0](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v2.0.0...v2.1.0) (2026-08-10)


### Features

* **editor:** realtime drags on a local composite, multi-select move, a colour picker, double-click placement, cursor zoom ([dac7c8e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/dac7c8e43aa7fb68751b9736194bd3cdc1554d9d))
* **release:** one visible executable, everything else inside bin\ ([dac7c8e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/dac7c8e43aa7fb68751b9736194bd3cdc1554d9d))


### Performance

* **client:** one-copy frame assembly and a cool idle editor ([dac7c8e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/dac7c8e43aa7fb68751b9736194bd3cdc1554d9d))
* **engine:** row-band parallel RenderFH6, bit-identical to the serial loop ([dac7c8e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/dac7c8e43aa7fb68751b9736194bd3cdc1554d9d))


### Bug Fixes

* **editor:** bank shapes arrive visible, selected and at full alpha ([dac7c8e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/dac7c8e43aa7fb68751b9736194bd3cdc1554d9d))
* **engine:** renders no longer block the service pipe ([dac7c8e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/dac7c8e43aa7fb68751b9736194bd3cdc1554d9d))

## [2.0.0](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v1.4.0...v2.0.0) (2026-08-07)


### ⚠ BREAKING CHANGES

* **engine:** count the background rectangle against the shape budget
* **engine:** the engine service no longer accepts a TCP connection or an auth token -- it reads a client on stdin and writes to stdout, so anything that dialled it must spawn it instead. The "Run as administrator" control is gone along with the elevation it triggered. The shipped build no longer contains the neural proposer; build with -tags aimodel to get it back.
* **engine:** run the engine as a service the client talks to
* **engine:** solve layer colour and alpha exactly on the finished stack
* **backend:** score every candidate with the same eval kernel in the studio and the CLI
* **backend:** drop the CPU backend, promote the pure-Go math to a test-only reference
* build and ship the release without the CUDA toolkit
* **vulkan:** run the full pipeline on Vulkan and make it the supported backend

* **backend:** drop the CPU backend, promote the pure-Go math to a test-only reference ([d12789f](https://github.com/HorizonRepublic/fh6-paint-studio/commit/d12789f419937641c267947b918204fcc70df026))
* build and ship the release without the CUDA toolkit ([d12789f](https://github.com/HorizonRepublic/fh6-paint-studio/commit/d12789f419937641c267947b918204fcc70df026))


### Features

* **ci:** manual publish_nexus dispatch to republish a tag to Nexus Mods ([48f3238](https://github.com/HorizonRepublic/fh6-paint-studio/commit/48f3238f5d1de3c858ce8221794f9e6f59aaf67e))
* **client:** the canvas-first interface, in Flutter ([c8aa59b](https://github.com/HorizonRepublic/fh6-paint-studio/commit/c8aa59b3e6cb3422ee3e57a7cd9ffb9e3de8db1d))
* **cli:** record the fitted source rectangle beside the geometry ([05ff48e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/05ff48ef678fa9745fe788c716fcd0b2ef226b0d))
* **editor:** layers, the in-game shape bank, and shape creation ([c8aa59b](https://github.com/HorizonRepublic/fh6-paint-studio/commit/c8aa59b3e6cb3422ee3e57a7cd9ffb9e3de8db1d))
* **engine:** region-aware terms and refit passes for the quality campaign ([d12789f](https://github.com/HorizonRepublic/fh6-paint-studio/commit/d12789f419937641c267947b918204fcc70df026))
* **engine:** run the engine as a service the client talks to ([c8aa59b](https://github.com/HorizonRepublic/fh6-paint-studio/commit/c8aa59b3e6cb3422ee3e57a7cd9ffb9e3de8db1d))
* **engine:** run the engine as a service the UI talks to ([86c5303](https://github.com/HorizonRepublic/fh6-paint-studio/commit/86c5303b0f2109ec4e3282aefb0fe47a52d59c78))
* **engine:** solve layer colour and alpha exactly on the finished stack ([05ff48e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/05ff48ef678fa9745fe788c716fcd0b2ef226b0d))
* **library:** sort, group by day, and delete runs in bulk ([c8aa59b](https://github.com/HorizonRepublic/fh6-paint-studio/commit/c8aa59b3e6cb3422ee3e57a7cd9ffb9e3de8db1d))
* **pixel:** pixel-art reconstruction without the engine ([d12789f](https://github.com/HorizonRepublic/fh6-paint-studio/commit/d12789f419937641c267947b918204fcc70df026))
* **studio:** pixel-art preset, native-resolution fit and a best-of control ([d12789f](https://github.com/HorizonRepublic/fh6-paint-studio/commit/d12789f419937641c267947b918204fcc70df026))
* **studio:** select the engine driver with FH6_ENGINE ([86c5303](https://github.com/HorizonRepublic/fh6-paint-studio/commit/86c5303b0f2109ec4e3282aefb0fe47a52d59c78))
* **vulkan:** feed the generator a structure-tensor coherence map ([05ff48e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/05ff48ef678fa9745fe788c716fcd0b2ef226b0d))
* **vulkan:** run the full pipeline on Vulkan and make it the supported backend ([d12789f](https://github.com/HorizonRepublic/fh6-paint-studio/commit/d12789f419937641c267947b918204fcc70df026))


### Performance

* **engine:** stop burning CPU while the GPU works ([d12789f](https://github.com/HorizonRepublic/fh6-paint-studio/commit/d12789f419937641c267947b918204fcc70df026))


### Bug Fixes

* **backend:** score every candidate with the same eval kernel in the studio and the CLI ([d12789f](https://github.com/HorizonRepublic/fh6-paint-studio/commit/d12789f419937641c267947b918204fcc70df026))
* **canvas:** split the compare wipe at one exact pixel ([c8aa59b](https://github.com/HorizonRepublic/fh6-paint-studio/commit/c8aa59b3e6cb3422ee3e57a7cd9ffb9e3de8db1d))
* **ci:** forward NEXUS_API_KEY to the reusable release build ([48f3238](https://github.com/HorizonRepublic/fh6-paint-studio/commit/48f3238f5d1de3c858ce8221794f9e6f59aaf67e))
* **cli:** decode geometry colour into the working space when scoring ([05ff48e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/05ff48ef678fa9745fe788c716fcd0b2ef226b0d))
* **client:** let the same run be injected again ([46842c5](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46842c5d57ec1375cec32d03f6625fff3d9dd8de))
* **client:** stopping a run no longer bleeds into the next one ([c8aa59b](https://github.com/HorizonRepublic/fh6-paint-studio/commit/c8aa59b3e6cb3422ee3e57a7cd9ffb9e3de8db1d))
* **deps:** update module gioui.org to v0.10.1 ([#40](https://github.com/HorizonRepublic/fh6-paint-studio/issues/40)) ([745ec65](https://github.com/HorizonRepublic/fh6-paint-studio/commit/745ec65596632035f0fad9a94f3756447c123ce2))
* **deps:** update module golang.org/x/image to v0.44.0 ([#43](https://github.com/HorizonRepublic/fh6-paint-studio/issues/43)) ([cff2962](https://github.com/HorizonRepublic/fh6-paint-studio/commit/cff2962a988af0611911bebbb494c3e2ce016f83))
* **deps:** update module golang.org/x/sys to v0.47.0 ([#42](https://github.com/HorizonRepublic/fh6-paint-studio/issues/42)) ([7f4f56b](https://github.com/HorizonRepublic/fh6-paint-studio/commit/7f4f56b49779c1e7d0aed19458cd42c3a1b3cc52))
* **deps:** update module golang.org/x/text to v0.39.0 ([#41](https://github.com/HorizonRepublic/fh6-paint-studio/issues/41)) ([efaa4fb](https://github.com/HorizonRepublic/fh6-paint-studio/commit/efaa4fb2902830925df3bbf3e2f25dfa3812deaf))
* **deps:** update module golang.org/x/text to v0.40.0 ([#44](https://github.com/HorizonRepublic/fh6-paint-studio/issues/44)) ([7ffebe5](https://github.com/HorizonRepublic/fh6-paint-studio/commit/7ffebe5bb10db2d55a245f5d9ed0fed8d4464d70))
* **engine:** count the background rectangle against the shape budget ([46842c5](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46842c5d57ec1375cec32d03f6625fff3d9dd8de))
* **engine:** honour the preset alpha floor in the polish ([05ff48e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/05ff48ef678fa9745fe788c716fcd0b2ef226b0d))
* **engine:** restore the quality default, draw line art, and drop every privilege the app never needed ([2b32876](https://github.com/HorizonRepublic/fh6-paint-studio/commit/2b32876043bc5f00f29d9b24c7a49602999da99d))
* **inject:** blank the template layers a fit did not fill ([c8aa59b](https://github.com/HorizonRepublic/fh6-paint-studio/commit/c8aa59b3e6cb3422ee3e57a7cd9ffb9e3de8db1d))
* **vulkan:** score dictionary words instead of rejecting them ([05ff48e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/05ff48ef678fa9745fe788c716fcd0b2ef226b0d))

## [1.4.0](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v1.3.0...v1.4.0) (2026-06-30)


### Features

* **editor:** coalesced visual undo/redo and a shortcuts legend ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))
* **editor:** drag shapes from the bank onto the canvas ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))
* **editor:** in-app vinyl shape editor with palette, layers and inspector ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))
* **editor:** multi-select with group move, rotate and scale ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))
* **editor:** per-layer lock, HSV colour wheel and recent colours ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))
* **editor:** smart snapping, symmetry stamping, array and radial duplicate ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))
* **engine:** glyph pre-pass, saliency quota and high-resolution fits ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))
* **i18n:** twelve-language UI with OS-locale detection and CJK fonts ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))
* **maskbank:** vector-exact dictionary masks ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))


### Performance

* **editor:** refresh only the dragged region for smooth dragging ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))


### Bug Fixes

* **deps:** update module golang.org/x/image to v0.43.0 ([#33](https://github.com/HorizonRepublic/fh6-paint-studio/issues/33)) ([a92f8b7](https://github.com/HorizonRepublic/fh6-paint-studio/commit/a92f8b7ec711640ab63e2d50c5a98afce65a3874))
* **editor:** keyboard shortcuts, one-click delete and undo reliability ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))
* **inject:** enable SeDebugPrivilege for elevated injection ([46abca0](https://github.com/HorizonRepublic/fh6-paint-studio/commit/46abca0bab4c196e63f88fdb5d7f3c5196b63676))

## [1.3.0](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v1.2.0...v1.3.0) (2026-06-10)


### Features

* cleaner anime shading and crisper line work — two new perceptual terms in the polishing stage cut visible banding by 10-15% and sharpen fine cel detail ([98e58db](https://github.com/HorizonRepublic/fh6-paint-studio/commit/98e58db43c966af7c50d88d79fe954bb83e9e4ee))
* line-art ink strokes now match the source line weight (previously drawn ~3x too thick) ([98e58db](https://github.com/HorizonRepublic/fh6-paint-studio/commit/98e58db43c966af7c50d88d79fe954bb83e9e4ee))
* photostock PNGs with a baked-in checkerboard "transparency" are detected and loaded as real cutouts ([98e58db](https://github.com/HorizonRepublic/fh6-paint-studio/commit/98e58db43c966af7c50d88d79fe954bb83e9e4ee))
* shapes export with sub-degree rotation — long thin strokes render continuous instead of slightly stair-stepped ([98e58db](https://github.com/HorizonRepublic/fh6-paint-studio/commit/98e58db43c966af7c50d88d79fe954bb83e9e4ee))
* smoother tonal ramps in the anime and photo modes ([98e58db](https://github.com/HorizonRepublic/fh6-paint-studio/commit/98e58db43c966af7c50d88d79fe954bb83e9e4ee))


### Bug Fixes

* line-art generations no longer pick up wide soft "gradient" smears from oversized outline stamps ([98e58db](https://github.com/HorizonRepublic/fh6-paint-studio/commit/98e58db43c966af7c50d88d79fe954bb83e9e4ee))
* polishing no longer silently stalls on full-budget generations — line art and dense anime finish up to 27% closer to the source ([98e58db](https://github.com/HorizonRepublic/fh6-paint-studio/commit/98e58db43c966af7c50d88d79fe954bb83e9e4ee))
* the estimated time remaining is a steady countdown instead of jumping back and forth by seconds ([98e58db](https://github.com/HorizonRepublic/fh6-paint-studio/commit/98e58db43c966af7c50d88d79fe954bb83e9e4ee))

## [1.2.0](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v1.1.0...v1.2.0) (2026-06-09)


### Features

* Line-art and anime stylizer — turn an image into clean vector line-art or anime-style vinyls
* Hybrid mode that lays ink lines over a geometrize fill
* Single-colour mode for clean one-colour logos and decals
* A built-in Library to save, browse and reuse past generations
* A crop tool to focus generation on part of an image
* Runs on AMD and Intel GPUs, not just NVIDIA
* Pick the GPU engine (CUDA or Vulkan) in the studio


### Performance

* Faster refinement, so generations finish quicker

## [1.1.0](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v1.0.1...v1.1.0) (2026-06-04)


### Features

* expert mode ([#20](https://github.com/HorizonRepublic/fh6-paint-studio/issues/20)) ([563fcaf](https://github.com/HorizonRepublic/fh6-paint-studio/commit/563fcaf04717f10b3ca1fd22d0c5d5c6df79fa7f))

## [1.0.1](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v1.0.0...v1.0.1) (2026-06-04)


### Bug Fixes

* exclude the in-app update check from release builds ([#14](https://github.com/HorizonRepublic/fh6-paint-studio/issues/14)) ([fb755cf](https://github.com/HorizonRepublic/fh6-paint-studio/commit/fb755cf17009f15048c9eaeb64134428296bc032))
* keep-inside no longer over-smooths the reconstruction ([#18](https://github.com/HorizonRepublic/fh6-paint-studio/issues/18)) ([6df818e](https://github.com/HorizonRepublic/fh6-paint-studio/commit/6df818e080158f43db72574c7e388ccac715038e))

## [1.0.0](https://github.com/HorizonRepublic/fh6-paint-studio/compare/v0.1.0...v1.0.0) (2026-06-04)


### Features

* faster generation via moment-seeded shape search ([9370207](https://github.com/HorizonRepublic/fh6-paint-studio/commit/9370207b16c57237c3ffa7506e52c9d08d800f81))
* Gaussian soft-glow mode for smooth and gradient images ([9370207](https://github.com/HorizonRepublic/fh6-paint-studio/commit/9370207b16c57237c3ffa7506e52c9d08d800f81))
* in-app update notifications with an About dialog ([9370207](https://github.com/HorizonRepublic/fh6-paint-studio/commit/9370207b16c57237c3ffa7506e52c9d08d800f81))


### Bug Fixes

* correct CPU/CUDA backend label ([9370207](https://github.com/HorizonRepublic/fh6-paint-studio/commit/9370207b16c57237c3ffa7506e52c9d08d800f81))

## 0.1.0 (2026-06-02)


### Miscellaneous Chores

* re-cut a clean v0.1.0 release ([7693644](https://github.com/HorizonRepublic/fh6-paint-studio/commit/7693644d02b019a21b7e94eb9a2fd2a50b7e8e05))


### Continuous Integration

* add CI + release-please with auto-built release artifacts ([5687a76](https://github.com/HorizonRepublic/fh6-paint-studio/commit/5687a76f1fcdfc5229b16951ee02f1b7285fe43e))
