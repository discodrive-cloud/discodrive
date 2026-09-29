-- The last TOTP time step (unix time / 30) a code was accepted for. A code is valid
-- for ~90 s (±1 step of skew); remembering the step makes each code single-use:
-- only a step strictly after this one is accepted. NULL = no code used yet.
ALTER TABLE user_totp ADD COLUMN last_used_step bigint;
