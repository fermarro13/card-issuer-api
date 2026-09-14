ALTER TABLE bank.idempotency_records
  ADD COLUMN response_status smallint CHECK (response_status IS NULL OR response_status BETWEEN 200 AND 299),
  ADD COLUMN response_body jsonb CHECK (response_body IS NULL OR jsonb_typeof(response_body)='object');

CREATE FUNCTION bank.guard_public_reference_keys() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF TG_TABLE_NAME='entities' AND NEW.bank_reference<>OLD.bank_reference THEN
    RAISE EXCEPTION 'Bank reference is immutable';
  ELSIF TG_TABLE_NAME='clients' AND NEW.external_client_ref<>OLD.external_client_ref THEN
    RAISE EXCEPTION 'External client reference is immutable';
  ELSIF TG_TABLE_NAME='account_references' AND NEW.external_account_ref<>OLD.external_account_ref THEN
    RAISE EXCEPTION 'External account reference is immutable';
  ELSIF TG_TABLE_NAME='card_products' AND NEW.product_code<>OLD.product_code THEN
    RAISE EXCEPTION 'Product code is immutable';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_public_reference_keys BEFORE UPDATE ON bank.entities
  FOR EACH ROW EXECUTE FUNCTION bank.guard_public_reference_keys();
CREATE TRIGGER guard_public_reference_keys BEFORE UPDATE ON bank.clients
  FOR EACH ROW EXECUTE FUNCTION bank.guard_public_reference_keys();
CREATE TRIGGER guard_public_reference_keys BEFORE UPDATE ON bank.account_references
  FOR EACH ROW EXECUTE FUNCTION bank.guard_public_reference_keys();
CREATE TRIGGER guard_public_reference_keys BEFORE UPDATE ON bank.card_products
  FOR EACH ROW EXECUTE FUNCTION bank.guard_public_reference_keys();
