ALTER TABLE repo_records MODIFY COLUMN record_json String CODEC(LZ4);
