-- Objects pgdba_ro cannot read, for the "not readable" note of list_schemas and list_objects. no_usage has no
-- USAGE grant; no_select grants USAGE and SELECT on one table only. Nothing outside the two schemas is touched.
CREATE SCHEMA no_usage;
CREATE TABLE no_usage.hidden (id integer PRIMARY KEY);

CREATE SCHEMA no_select;
GRANT USAGE ON SCHEMA no_select TO pgdba_ro;
CREATE TABLE no_select.open_t (id integer PRIMARY KEY);
CREATE TABLE no_select.secret_t (id integer PRIMARY KEY);
GRANT SELECT ON no_select.open_t TO pgdba_ro;
