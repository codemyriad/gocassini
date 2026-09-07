### Changed
- Portable meetings identify their recording from canonical compressed Opus packets, making integrity checks independent of decoder implementation details.
- Portable packing now converges on FFmpeg's stable Ogg end-trim granule before embedding that compressed identity, including multi-speaker mixes whose first metadata remux normalizes a few trailing samples.
