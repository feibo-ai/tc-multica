BEGIN;

DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS integration_deployment;
DROP TABLE IF EXISTS integration_secret;
DROP TABLE IF EXISTS integration;

COMMIT;
