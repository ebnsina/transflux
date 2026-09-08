-- A sprite sheet is useless without the index that says which region of it
-- belongs to which moment, so the index is a kind of artifact in its own right
-- rather than a subtitle that happens to contain image references.
alter table artifacts drop constraint artifacts_kind_check;
alter table artifacts add constraint artifacts_kind_check check (kind in (
    'rendition', 'segment_set', 'manifest', 'thumbnail', 'sprite', 'sprite_index',
    'poster', 'subtitle', 'transcript', 'clip', 'drm_metadata', 'log'));
