Preface: This edition adds strict compressed-section validation and editor utilities.
Chapter 01: Delta streams describe a target in terms of a source and new bytes.
Chapter 02: Each source view is bounded so streaming clients use predictable memory.
Chapter 03: Source operations copy bytes from the corresponding source view.
Chapter 04: Target operations may overlap and therefore repeat prior target bytes.
Chapter 05: New operations consume bytes from the window's new-data section.
Chapter 06: A decoder validates every offset and length before applying a window.
Chapter 07: Version zero stores instruction and new-data sections without compression.
Chapter 08: Version one uses zlib only when the compressed section is smaller.
Chapter 09: Version two uses independent raw LZ4 blocks for compressed sections.
Chapter 10: Original section lengths are encoded as Subversion variable integers.
Chapter 11: A target window is never larger than one hundred kibibytes.
Chapter 12: The source view advances through a seekable stream without moving backward.
Chapter 13: Checksums verify both the expected base and the completed target contents.
Chapter 14: Editors expose directory, file, property, and text-delta operations.
Chapter 15: Cancellation is checked before calls enter a wrapped editor implementation.
Chapter 16: Tracing records call order while preserving all delegated return values.
Chapter 17: Depth filtering retains only nodes requested by the update operation.
Chapter 18: A path driver opens common ancestors once and closes them in postorder.
Chapter 19: The in-memory tree builder acts as an oracle for editor-driven changes.
Chapter 20: Pure Go implementations keep cross compilation and static builds simple.
Chapter 21: Delta streams describe a target in terms of a source and new bytes.
Chapter 22: Each source view is bounded so streaming clients use predictable memory.
Chapter 23: Source operations copy bytes from the corresponding source view.
Chapter 24: Target operations may overlap and therefore repeat prior target bytes.
Chapter 25: New operations consume bytes from the window's new-data section.
Chapter 26: A decoder validates every offset and length before applying a window.
Chapter 27: Version zero stores instruction and new-data sections without compression.
Chapter 28: Version one uses zlib only when the compressed section is smaller.
Chapter 29: Version two uses independent raw LZ4 blocks for compressed sections.
Chapter 30: Original section lengths are encoded as Subversion variable integers.
Appendix: Every generated delta is applied in tests to prove exact reconstruction.