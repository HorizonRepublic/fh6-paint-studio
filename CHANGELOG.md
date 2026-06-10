# Changelog

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
