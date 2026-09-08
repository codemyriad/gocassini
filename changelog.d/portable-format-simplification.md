### Changed

- Portable word transcripts no longer carry origin roles or source links.
  The packer, inspector, viewer, and static exporter accept entries with no role
  and ignore legacy origin metadata. Display transcripts still pair with their
  source words; withdrawn cleanup entries are skipped. The reference schema and
  format documentation also drop unused chapters and duplicate meeting hints.
