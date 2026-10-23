-- Objects of the audit-tools change, in their own schemas: a materialized view
-- and a foreign key across schemas (audit_tools_b.child -> audit_tools.parent).
CREATE SCHEMA audit_tools;
CREATE SCHEMA audit_tools_b;

CREATE TABLE audit_tools.parent (
    id   integer PRIMARY KEY,
    name text NOT NULL
);
INSERT INTO audit_tools.parent SELECT i, 'parent ' || i FROM generate_series(1, 20) AS i;

CREATE TABLE audit_tools_b.child (
    id        integer PRIMARY KEY,
    parent_id integer NOT NULL REFERENCES audit_tools.parent (id) ON DELETE CASCADE
);
INSERT INTO audit_tools_b.child SELECT i, 1 + i % 20 FROM generate_series(1, 40) AS i;

CREATE MATERIALIZED VIEW audit_tools.parent_names AS
SELECT id, name FROM audit_tools.parent;
COMMENT ON MATERIALIZED VIEW audit_tools.parent_names IS 'cached parent names';

ANALYZE audit_tools.parent, audit_tools_b.child;
ANALYZE audit_tools.parent_names;

GRANT USAGE ON SCHEMA audit_tools, audit_tools_b TO pgdba_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA audit_tools, audit_tools_b TO pgdba_ro;
