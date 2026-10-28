-- Context line of get_top_queries when pg_stat_statements_info is absent (extension before 1.9): the reset time is unknown.
SELECT current_database()::text, current_user::text, 'unknown'
