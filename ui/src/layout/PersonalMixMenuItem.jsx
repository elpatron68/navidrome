import React, { forwardRef } from 'react'
import { useDispatch } from 'react-redux'
import {
  MenuItem,
  ListItemIcon,
  Tooltip,
  useMediaQuery,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import { setSidebarVisibility, useNotify, useTranslate } from 'react-admin'
import { MdAutoAwesome } from 'react-icons/md'
import { playPersonalMix } from '../common/playbackActions'
import config from '../config'

const useStyles = makeStyles((theme) => ({
  icon: { minWidth: theme.spacing(5) },
  open: { paddingLeft: theme.spacing(4) },
  closed: { paddingLeft: theme.spacing(2) },
}))

// PersonalMixMenuItem is a sidebar entry (shown under the Albums submenu) that, unlike the album
// list links, triggers playback of a habit-based Personal Mix instead of navigating.
const PersonalMixMenuItem = forwardRef(({ sidebarIsOpen, dense }, ref) => {
  const translate = useTranslate()
  const dispatch = useDispatch()
  const notify = useNotify()
  const classes = useStyles()
  const isSmall = useMediaQuery((theme) => theme.breakpoints.down('sm'))

  if (!config.personalMixEnabled) {
    return null
  }

  const label = translate('resources.song.actions.personalMix')

  const handleClick = async () => {
    notify('message.buildingPersonalMix', 'info')
    try {
      await playPersonalMix(dispatch, notify, { size: 100 })
    } catch (e) {
      notify('ra.page.error', 'warning')
    }
    if (isSmall) {
      dispatch(setSidebarVisibility(false))
    }
  }

  const item = (
    <MenuItem
      ref={ref}
      dense={dense}
      button
      onClick={handleClick}
      className={sidebarIsOpen ? classes.open : classes.closed}
    >
      <ListItemIcon className={classes.icon}>
        <MdAutoAwesome size={22} />
      </ListItemIcon>
      {sidebarIsOpen && label}
    </MenuItem>
  )

  return sidebarIsOpen ? (
    item
  ) : (
    <Tooltip title={label} placement="right">
      {item}
    </Tooltip>
  )
})

PersonalMixMenuItem.displayName = 'PersonalMixMenuItem'

export default PersonalMixMenuItem
