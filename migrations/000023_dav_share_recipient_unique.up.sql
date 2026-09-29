-- One share per (calendar or address book, recipient): sharing again updates the share
-- instead of adding a second copy of the collection to the recipient's home set.
-- Duplicates keep the longest-lived row; self-shares and shares (and feed links) left
-- behind by deleted collections go.
DELETE FROM resource_shares
WHERE resource_type IN ('calendar', 'addressbook') AND shared_with_user = owner_id;

DELETE FROM resource_shares s
WHERE (s.resource_type = 'calendar' AND NOT EXISTS (SELECT 1 FROM calendars c WHERE c.id = s.resource_id))
   OR (s.resource_type = 'addressbook' AND NOT EXISTS (SELECT 1 FROM addressbooks a WHERE a.id = s.resource_id));

DELETE FROM resource_shares WHERE id IN (
    SELECT id FROM (
        SELECT id, row_number() OVER (
            PARTITION BY resource_type, resource_id, shared_with_user
            ORDER BY expires_at DESC NULLS FIRST, created_at DESC, id DESC) AS rn
        FROM resource_shares
        WHERE shared_with_user IS NOT NULL AND resource_type IN ('calendar', 'addressbook')
    ) d WHERE d.rn > 1);

CREATE UNIQUE INDEX resource_shares_dav_recipient
    ON resource_shares (resource_type, resource_id, shared_with_user)
    WHERE shared_with_user IS NOT NULL AND resource_type IN ('calendar', 'addressbook');

-- A calendar PUT looks for another object with the same iCalendar UID (RFC 4791
-- no-uid-conflict); without this index that is a scan of the whole calendar per write.
-- Not unique: collections written before this check may already hold duplicates.
CREATE INDEX calendar_objects_ical_uid ON calendar_objects (calendar_id, (parsed ->> 'uid'));

-- The same for address books: a card PUT looks for another card with the same vCard UID
-- (RFC 6352 §6.3.2.1 no-uid-conflict).
CREATE INDEX addressbook_objects_vcard_uid ON addressbook_objects (addressbook_id, (parsed ->> 'uid'));
