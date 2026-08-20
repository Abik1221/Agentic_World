DROP TABLE IF EXISTS lab_certificate;
DROP TABLE IF EXISTS lab_certify_payoff;
DROP TABLE IF EXISTS lab_fit_counts;
DROP TABLE IF EXISTS lab_cert_run;
DROP TRIGGER IF EXISTS lab_ladder_spec_immutable ON lab_ladder_spec;
DROP FUNCTION IF EXISTS lab_ladder_spec_is_immutable();
DROP TABLE IF EXISTS lab_ladder_spec;
