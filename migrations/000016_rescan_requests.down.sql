DROP TRIGGER IF EXISTS rescan_requests_notify ON rescan_requests;
DROP FUNCTION IF EXISTS notify_rescan_request();
DROP TABLE IF EXISTS rescan_requests;
