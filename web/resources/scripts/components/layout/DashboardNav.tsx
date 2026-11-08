import React from 'react';
import { faLayerGroup } from '@fortawesome/free-solid-svg-icons';
import SidebarLink, { SidebarHeading } from '@/components/layout/SidebarLink';
import routes from '@/routers/routes';

export default () => (
    <>
        <SidebarLink to={'/'} exact icon={faLayerGroup}>
            Servers
        </SidebarLink>
        <SidebarHeading>Account</SidebarHeading>
        {routes.account
            .filter((route) => !!route.name)
            .map(({ path, name, exact = false, icon }) => (
                <SidebarLink key={path} to={`/account/${path}`.replace('//', '/')} exact={exact} icon={icon}>
                    {name}
                </SidebarLink>
            ))}
    </>
);
