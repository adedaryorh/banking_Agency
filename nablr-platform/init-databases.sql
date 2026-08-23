CREATE DATABASE nabla_identity;
CREATE DATABASE nabla_notification;
CREATE DATABASE nabla_transfers;

\connect nabla_identity
\i /schemas/identity-schema.sql

\connect nabla_notification
\i /schemas/notification-schema.sql

\connect nabla_transfers
\i /schemas/transfers-schema.sql
