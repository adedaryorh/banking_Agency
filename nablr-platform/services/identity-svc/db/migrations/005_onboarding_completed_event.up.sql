CREATE OR REPLACE FUNCTION emit_onboarding_completed_event()
RETURNS trigger AS $$
BEGIN
    IF NEW.password_set = true AND NEW.status = 'active'
       AND (OLD.password_set IS DISTINCT FROM true OR OLD.status IS DISTINCT FROM 'active') THEN
        INSERT INTO outbox_events (
            exchange, routing_key, event_type, aggregate_type, aggregate_id, payload
        ) VALUES (
            'nabla.events', 'identity.onboarding.completed',
            'identity.onboarding.completed', 'user', NEW.id,
            jsonb_build_object('user_id', NEW.id, 'currency', 'NGN')
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_onboarding_completed ON users;
CREATE TRIGGER trg_users_onboarding_completed
AFTER UPDATE OF password_set, status ON users
FOR EACH ROW EXECUTE FUNCTION emit_onboarding_completed_event();
