ALTER TABLE jobs ADD COLUMN room_token TEXT;
ALTER TABLE jobs ADD COLUMN room_name TEXT;

-- Recover the room for every job the operator has already run. Until now the
-- room existed only inside the opaque talk_binding blob, so reading it meant a
-- JSON parse from whatever language happened to be asking. room_name is absent
-- from a binding whose name lookup never completed and from every binding
-- written before D-622, so json_extract returns NULL there rather than ''.
UPDATE jobs
SET room_token = json_extract(talk_binding, '$.room_token'),
    room_name  = json_extract(talk_binding, '$.room_name')
WHERE talk_binding IS NOT NULL
  AND json_valid(talk_binding);
