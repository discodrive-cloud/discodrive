-- Apple's calendar-order, set by PROPPATCH. NULL until a client sets it.
ALTER TABLE calendars ADD COLUMN sort_order integer;
