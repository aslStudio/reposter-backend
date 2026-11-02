DELETE FROM routes r USING sources s, destinations d
WHERE r.source_id = s.id AND r.destination_id = d.id
  AND s.platform = 'x' AND s.handle = 'foxnews'
  AND d.platform = 'x' AND lower(d.external_id) = '@asldigitalvlad';

DELETE FROM sources s
WHERE s.platform = 'x' AND s.handle = 'foxnews'
  AND NOT EXISTS (SELECT 1 FROM routes r WHERE r.source_id = s.id);
