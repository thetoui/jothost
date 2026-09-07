-- Removes DNS templates.
--
-- The zones themselves are untouched: a template only ever seeds records at
-- creation, and the records it wrote belong to the zone afterwards. Dropping
-- these tables takes away the ability to customise what a new zone starts
-- with, and returns the panel to the hard-coded apex-and-www seed.

DROP TABLE IF EXISTS dns_template_records;
DROP TABLE IF EXISTS dns_templates;
