# Get-Role Module

The **Get-Role** module allows server administrators to create interactive embedded messages containing buttons that users can click to self-assign roles. 

## Features

- **Interactive Role Menus**: Generates an embedded message with up to 5 buttons, each corresponding to a Discord role.
- **State Persistence**: Uses a SQLite database table (`role_menus`) to link the randomly generated button IDs to the specific Discord Role IDs and Guild IDs.
- **Toggle Behavior**: Clicking a role button will add the role if the user doesn't have it, and remove it if they do.

## Commands

### `/rolemenu`

Creates a new role assignment menu.
- **Permissions Required**: Manage Channels
- **Options**:
  - `title` (Required): The title of the embed message.
  - `description` (Required): Description of the embed message.
  - `role1` (Required): The first role option to include as a button.
  - `role2` to `role5` (Optional): Additional role options.

## Database

The module automatically initializes a `role_menus` table in the SQLite database upon startup:
- `button_id` (TEXT, Primary Key)
- `role_id` (TEXT)
- `guild_id` (TEXT)
