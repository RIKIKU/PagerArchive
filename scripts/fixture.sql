-- The standard test fixture (SPEC.md section 11): the template "Example
-- Fire" with the example agency profile, and Stations 1-4 on 0100001-0100004.
-- Loaded with psql after the API has created the tables; admins manage
-- templates and aliases with the PowerShell module or the browser (R59).
INSERT INTO templates (name, agency, colour, agency_profile)
VALUES ('Example Fire', 'Example Fire Service', '#b3261e', 'example-fire');

INSERT INTO aliases (pattern, name, template_id)
SELECT '010000' || n, 'Station ' || n, t.id
FROM templates t, generate_series(1, 4) AS n
WHERE t.name = 'Example Fire';
