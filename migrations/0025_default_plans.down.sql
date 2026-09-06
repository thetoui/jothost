-- Removes only the four seeded plans, and only if nothing was ever sold on
-- them.
--
-- A plan with a subscription against it is somebody's billing arrangement, not
-- a fixture: the foreign key would refuse the delete anyway, and doing this by
-- name rather than by "everything" means an operator's own plans are never
-- touched by a rollback.
DELETE FROM service_plans
WHERE owner_user_id IS NULL
  AND name IN ('Starter', 'Personal', 'Business', 'Unlimited')
  AND NOT EXISTS (
      SELECT 1 FROM subscriptions WHERE subscriptions.plan_id = service_plans.id
  );
