CREATE OR REPLACE FUNCTION bank.guard_public_reference_keys() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE
  protected_column text;
  immutable_message text;
BEGIN
  CASE TG_TABLE_NAME
    WHEN 'entities' THEN
      protected_column := 'bank_reference';
      immutable_message := 'Bank reference is immutable';
    WHEN 'clients' THEN
      protected_column := 'external_client_ref';
      immutable_message := 'External client reference is immutable';
    WHEN 'account_references' THEN
      protected_column := 'external_account_ref';
      immutable_message := 'External account reference is immutable';
    WHEN 'card_products' THEN
      protected_column := 'product_code';
      immutable_message := 'Product code is immutable';
  END CASE;

  IF protected_column IS NOT NULL
     AND (to_jsonb(NEW)->>protected_column) IS DISTINCT FROM (to_jsonb(OLD)->>protected_column) THEN
    RAISE EXCEPTION '%', immutable_message;
  END IF;
  RETURN NEW;
END $$;
