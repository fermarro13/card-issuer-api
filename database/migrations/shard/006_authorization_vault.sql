ALTER TABLE bank.cards ADD COLUMN masked_pan text;
ALTER TABLE bank.cards DROP COLUMN credential_reference;

CREATE FUNCTION bank.guard_masked_pan() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF OLD.masked_pan IS NOT NULL AND NEW.masked_pan IS DISTINCT FROM OLD.masked_pan THEN
    RAISE EXCEPTION 'Masked PAN is immutable';
  END IF;
  IF NEW.masked_pan IS NOT NULL AND NEW.masked_pan !~ '^[0-9]{6}\*{6}[0-9]{4}$' THEN
    RAISE EXCEPTION 'Masked PAN has an invalid format';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_masked_pan BEFORE UPDATE ON bank.cards
  FOR EACH ROW EXECUTE FUNCTION bank.guard_masked_pan();

CREATE TABLE bank.authorization_verifications (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  decision_id uuid NOT NULL,
  bank_transaction_reference text NOT NULL CHECK (btrim(bank_transaction_reference)<>''),
  card_id uuid,
  decision text NOT NULL CHECK (decision IN ('approved','declined')),
  decision_code text NOT NULL CHECK (decision_code IN ('approved','credential_invalid','card_not_found','card_ineligible')),
  decided_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,bank_transaction_reference),
  UNIQUE (entity_id,decision_id),
  FOREIGN KEY (entity_id,card_id) REFERENCES bank.cards(entity_id,id),
  CHECK ((decision='approved' AND decision_code='approved' AND card_id IS NOT NULL) OR
         (decision='declined' AND decision_code<>'approved'))
);

ALTER TABLE bank.authorization_verifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.authorization_verifications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.authorization_verifications
  USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.authorization_verifications
  FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

CREATE INDEX authorization_verifications_time
  ON bank.authorization_verifications(entity_id,created_at DESC,id DESC);
GRANT SELECT,INSERT ON bank.authorization_verifications TO ci_business_runtime;
